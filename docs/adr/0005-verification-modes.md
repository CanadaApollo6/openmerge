# 0005. Offer post-merge verification as a per-queue mode alongside strict landing

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

OpenMerge's first principle has been "what you tested is what you merged": a queue lands only the exact commit CI tested ([ADR 0004](0004-forge-driver-interface.md)). That guarantee costs throughput, because every landing waits for CI on a commit built on the current base. Our simulator shows the ceiling: at 2,000 PRs a day with 30-minute CI, batches of up to eight still had a median time to merge of 6.7 days. Phase 2 speculation raises the ceiling but does not remove it.

Agent-scale users have already left this model. Anthropic reports growing from about 2,500 to over 30,000 merges a day between May and September 2026, and writes that "a proper merge queue, for example, doesn't make much sense at agent scale" and asks "what does CI even mean when hundreds or even thousands of changes can merge while tests are running?" It relies instead on "post-merge and pre-deploy automated testing", automated bisect and revert pipelines, and agents that fix conflicts as they appear ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control), 2026-10-13; see also [landscape research](../research/landscape.md)). Chromium sheriffing and Google TAP culprit finding are older versions of the same idea.

## Decision

### Two modes

**Verification is a per-queue setting.** `verification: strict` is the default and keeps today's behavior exactly: the landed commit is byte-for-byte the tested commit, and Phase 2 speculation applies. `verification: post_merge` is opt-in. Every status and API response reports the queue's mode.

**Post-merge settings** (rejected by validation in a strict queue):

| Key | Shape | Default | Meaning |
|---|---|---|---|
| `max_base_age` | duration, at least the check timeout | `1h` | How long ago the admission test's base may have been the tip, checked again at landing |
| `on_red` | `revert` \| `pause` \| `notify` | `revert` | Remedy for a confirmed red commit |
| `max_red` | duration | `30m` | Longest the base may stay red before ordinary landings pause |
| `max_remedies` | integer, 0 to 5 | `3` | Automatic remedies per red episode before escalating |

### Admission and landing

- **Admission.** A PR's required checks must pass on a test ref built from a pinned head on a base that was the tip within `max_base_age`. The trust, approval and condition gates are the strict ones.
- **Landing.** Before landing, OpenMerge rereads the PR and fails the attempt unless the head is still the pinned head, the trust gate passes freshly (approvals, permissions, fork rules), the conditions still hold, and the admission base is still within `max_base_age`. It then composes the pinned head onto the current tip with the configured merge method, giving the landing commit L, and lands L with the same atomic push leased on the base and the fence. A composition conflict is not landed (see Conflicts).
- **Durable record before the push.** The start event records the attempt ID, the pinned heads, the tested commit T, the base T was tested on, the composed L, and the queue's configuration generation. In post-merge queues the fence commit records both, as `land/<attempt> tested=<T> landed=<L>`. Fence recovery reads both fields, so a crash between push and record can never present L as tested or lose which commit landed.
- **Honest reporting.** The PR's check, the landed comment, status and API all say "tested as `T` on `<base>`, landed as `L`". They never describe L as tested.

### Verifying main

**Every landed commit is checked.** CI already runs on pushes to the base. A landed commit is green when all its required checks passed on that exact commit, and red when a required check failed twice: once, then once more after one automatic rerun to filter flakes.

**Verification frontier.** OpenMerge tracks, per base, the newest landed commit F such that every landed commit up to and including F has a final result: green, or red and resolved. A commit whose checks are still pending stops the frontier, even if later commits already passed. Red commits are recorded in an unresolved set that is independent of the frontier and the green ref; a red commit leaves the set only when it is resolved.

**Green ref.** `openmerge/green/<base>` is a protected, App-only ref that deploys follow instead of the base. OpenMerge moves it forward, by compare-and-swap along the base's first-parent history, to the newest green commit C at or behind the frontier while the unresolved set has no member at or before C. A red commit R is resolved only when a remedy for R (or any later fix) has landed and a green commit after it is inside the frontier. A later commit that passes while still containing R's bug, or while R's own checks are pending, cannot move the ref. The promise is that the green ref only points at commits whose required checks passed on that exact commit, with every earlier commit's result known and every failure resolved. It is not a promise that those checks catch every bug.

### Red commits

1. **Episode.** A red episode opens when a landed commit is confirmed red and no episode is open, and gets a durable ID. Every red commit until the episode closes belongs to it. It closes when the unresolved set is empty and the frontier holds a green commit after the last remedy.
2. **Suspect.** The suspect is the first red commit after the frontier. If CI skipped or cancelled runs for commits before it, for example through concurrency groups, OpenMerge runs CI on every one of those commits, in parallel, by pushing them to `openmerge/verify/<base>/<sha>`; it does not binary-search, because history need not be monotonic. A commit whose result still cannot be obtained makes the diagnosis inconclusive.
3. **Remedy.** With `on_red: revert`, OpenMerge composes a revert of the suspect and lands it ahead of ordinary PRs. The revert carries `Reverts:`, credits the suspect's author, and goes through the same landing path. With `pause`, ordinary landings pause. With `notify`, OpenMerge only reports.
4. **Verify the remedy.** A remedy is verified when its own commit is green. If the tip stays red after a verified remedy, OpenMerge looks for the next suspect: the first red commit after the remedy whose failure is not explained by an already-remedied suspect. Independent defects that land close together each get their own remedy.
5. **Bounded automation.** An episode gets at most `max_remedies` automatic remedies, and at most one per suspect. OpenMerge never automatically reverts a commit it authored as a remedy, and never retries a remedy that conflicts. An inconclusive diagnosis, a remedy that does not turn its own commit green, a conflicting revert, an exhausted `max_remedies`, or an episode older than `max_red` escalates: ordinary landings pause, culprit finding and verification keep running, and a human gets the evidence.
6. **Recovering from escalation.** A human fixes the base by landing a fix through the queue or pushing it directly. Each fix is just another landed commit. A writer's `@openmerge refresh` asks for the tip to be verified again; ordinary landings resume automatically only when the episode closes, never on the command alone.
7. **Report.** The suspect PR's author gets one structured comment and a machine-readable event: the commits, the failed checks, the evidence, and the remedy's SHA or the reason it escalated.

### Conflicts

A composition conflict produces a structured event naming the conflicting files and the base commit, and the PR returns to its author. Until change identity exists (Phase 3) the fixed push re-enters at the back of the queue. With change identity, the PR may hold its position for `conflict_grace`: one deadline set at the first conflict and never renewed, a bounded number of conflict retries, fresh checks and trust for each new head, and PRs behind it still land while it waits.

### Switching modes

A mode change is a new configuration generation. Admission under the old generation freezes. In-flight consequential actions finish or are fenced as today. Outstanding post-merge obligations (an open episode, unresolved red commits, pending reruns, verify runs, remedies) and any escalation pause belong to the queue, not to a mode, so they survive the switch and keep running under the generation that created them. The new mode admits work only after the episode closes and no escalation pause remains. Recovery evidence is tied to the persisted attempt's generation, not to the queue's current mode: the fence parser accepts both fence forms, then checks that the form matches the attempt it names, so switching modes can never hide an in-flight landing.

## Consequences

- Product principle 1 becomes a promise about honesty: strict queues land exactly what was tested; post-merge queues say exactly what was tested before landing and keep a green ref that only passing code reaches.
- Post-merge throughput is bounded by landing latency and conflict rate, not by CI duration. The base can be red between a bad landing and its remedy, so deploys must follow the green ref. Setup and the rules checks verify CI on base pushes and on `openmerge/verify/**`, and that `openmerge/green/*` and `openmerge/verify/*` are protected.
- CI cost per merged PR is about two runs (admission and post-merge), plus reruns and culprit finding. The simulator must model a finite runner pool so its exit tests measure latency under a CI budget, not with unlimited CI.
- Post-merge landings use new event types and a new fence-commit form. Existing event types and the strict fence form stay byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)); the shared fence parser accepts both forms and validates each against its persisted attempt.
- The simulator's invariants for post-merge queues change from "main is always green" to: the green ref only points at green commits inside the verification frontier with no unresolved red commit at or before them; every red episode closes or escalates; and no landing is reported as tested unless it was.
- [ADR 0004](0004-forge-driver-interface.md)'s "what lands is byte-for-byte what was tested" holds for strict queues only. The landing mechanism, one atomic push leased on base and fence, is unchanged in both modes.

## Alternatives considered

- **Strict only, with more speculation.** Speculation multiplies CI cost to buy throughput and still serializes on landing a tested commit. It stays the default for most teams, but it cannot reach agent-scale volume, and teams at that volume will route around a tool that refuses them.
- **Post-merge only.** Many teams cannot accept a red base even briefly, or deploy from the base directly. Removing strict mode would give up the guarantee the project started from.
- **Optimistic batching (Zuul's HEADGREEN) instead.** It tolerates rare intermittent failures but keeps CI-bound serialization, so it does not answer the scale problem. It remains a Phase 2 option within strict mode.
- **Leave culprit finding and reverts to external tooling.** Culprit finding needs the queue's own record of what landed when and what was tested where. Splitting it out loses that record and keeps the base red longer.
- **Automatic revert-of-revert or repeated remedies.** Each automatic step can create the next failure. One verified remedy, then a human, keeps automation from making things worse.
