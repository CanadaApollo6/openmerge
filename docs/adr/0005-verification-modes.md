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

### Admission and landing

- **Admission.** A PR's required checks must pass on a test ref built from a pinned head on a base that was the tip within `max_base_age`. The trust, approval and condition gates are the strict ones.
- **Landing.** Before landing, OpenMerge rereads the PR and fails the attempt unless the head is still the pinned head, the trust gate passes freshly (approvals, permissions, fork rules), the conditions still hold, and the admission base is still within `max_base_age`. It then composes the pinned head onto the current tip with the configured merge method, giving the landing commit L, and lands L with the same atomic push leased on the base and the fence. A composition conflict is not landed (see Conflicts).
- **Durable record before the push.** The start event records the attempt ID, the pinned heads, the tested commit T, the base T was tested on, the composed L, and the queue's configuration generation. In post-merge queues the fence commit records both, as `land/<attempt> tested=<T> landed=<L>`. Fence recovery reads both fields, so a crash between push and record can never present L as tested or lose which commit landed.
- **Honest reporting.** The PR's check, the landed comment, status and API all say "tested as `T` on `<base>`, landed as `L`". They never describe L as tested.

### Verifying main

**Every landed commit is checked.** CI already runs on pushes to the base. A landed commit is green when all its required checks passed on that exact commit, and red when a required check failed twice: once, then once more after one automatic rerun to filter flakes.

**Green ref.** `openmerge/green/<base>` is a protected, App-only ref that deploys follow instead of the base. OpenMerge moves it forward, by compare-and-swap along the base's first-parent history, to the newest commit C that:

1. is green itself, and
2. has no unresolved red commit between the current green ref and C.

A red commit R is resolved only when C's first-parent history contains a remedy for R landed after R, and C itself is green. A later commit that passes while still containing R's bug cannot move the ref, because R is unresolved. The promise is that the green ref only points at commits whose required checks passed on that exact commit, with every earlier failure resolved. It is not a promise that those checks catch every bug.

### Red commits

1. **Culprit.** If every landed commit has its own CI result, the first red commit after the green ref is the suspect and no bisect is needed. If the CI system skipped or cancelled runs for some commits, for example through concurrency groups, OpenMerge runs CI on just those commits by pushing them to `openmerge/verify/<base>/<sha>` and binary-searches the gap.
2. **Remedy.** With `on_red: revert`, OpenMerge composes a revert of the suspect and lands it ahead of ordinary PRs. The revert carries `Reverts:`, credits the suspect's author, and goes through the same landing path. With `pause`, ordinary landings pause. With `notify`, OpenMerge only reports.
3. **Verify the remedy.** A remedy counts only when the tip after it is green. If it is not, the diagnosis is inconclusive: two individually green changes that fail only together, a later change that masks the bug, or flaky evidence.
4. **Bounded automation.** OpenMerge makes at most one automatic remedy per red episode. It never automatically reverts a commit it authored as a remedy, and it never retries a remedy that conflicts. An inconclusive diagnosis, a conflicting revert, or a red base older than `max_red` pauses ordinary landings, keeps culprit finding and verification running, and asks a human with the evidence. A writer's `@openmerge refresh` resumes ordinary landings after a manual fix.
5. **Report.** The suspect PR's author gets one structured comment and a machine-readable event: the commits, the failed checks, the evidence, and the remedy's SHA or the reason it escalated.

### Conflicts

A composition conflict produces a structured event naming the conflicting files and the base commit, and the PR returns to its author. Until change identity exists (Phase 3) the fixed push re-enters at the back of the queue. With change identity, the PR may hold its position for `conflict_grace`: one deadline set at the first conflict and never renewed, a bounded number of conflict retries, fresh checks and trust for each new head, and PRs behind it still land while it waits.

### Switching modes

A mode change is a new configuration generation. Admission under the old generation freezes. In-flight consequential actions finish or are fenced as today. Outstanding post-merge obligations (unresolved red commits, pending reruns, culprit finding, remedies) continue under the generation that created them until they resolve or escalate. Only then does the new mode admit work.

## Consequences

- Product principle 1 becomes a promise about honesty: strict queues land exactly what was tested; post-merge queues say exactly what was tested before landing and keep a green ref that only passing code reaches.
- Post-merge throughput is bounded by landing latency and conflict rate, not by CI duration. The base can be red between a bad landing and its remedy, so deploys must follow the green ref. Setup and the rules checks verify CI on base pushes and on `openmerge/verify/**`, and that `openmerge/green/*` and `openmerge/verify/*` are protected.
- CI cost per merged PR is about two runs (admission and post-merge), plus reruns and culprit finding. The simulator must model a finite runner pool so its exit tests measure latency under a CI budget, not with unlimited CI.
- Post-merge landings use new event types and a new fence-commit form. Existing event types and the strict fence form stay byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)); the shared fence parser accepts the new form only for post-merge attempts.
- The simulator's invariants for post-merge queues change from "main is always green" to: the green ref only points at green commits with no unresolved red ancestor; every red episode ends in a verified remedy or an escalation; and no landing is reported as tested unless it was.
- [ADR 0004](0004-forge-driver-interface.md)'s "what lands is byte-for-byte what was tested" holds for strict queues only. The landing mechanism, one atomic push leased on base and fence, is unchanged in both modes.

## Alternatives considered

- **Strict only, with more speculation.** Speculation multiplies CI cost to buy throughput and still serializes on landing a tested commit. It stays the default for most teams, but it cannot reach agent-scale volume, and teams at that volume will route around a tool that refuses them.
- **Post-merge only.** Many teams cannot accept a red base even briefly, or deploy from the base directly. Removing strict mode would give up the guarantee the project started from.
- **Optimistic batching (Zuul's HEADGREEN) instead.** It tolerates rare intermittent failures but keeps CI-bound serialization, so it does not answer the scale problem. It remains a Phase 2 option within strict mode.
- **Leave culprit finding and reverts to external tooling.** Culprit finding needs the queue's own record of what landed when and what was tested where. Splitting it out loses that record and keeps the base red longer.
- **Automatic revert-of-revert or repeated remedies.** Each automatic step can create the next failure. One verified remedy, then a human, keeps automation from making things worse.
