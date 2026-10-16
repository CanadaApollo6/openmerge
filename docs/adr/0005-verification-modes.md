# 0005. Offer post-merge verification as a per-queue mode alongside strict landing

- **Status:** Accepted
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
| `max_red` | duration, at least 3x the check timeout | `2h` | Longest a suspect may stay open before it escalates |
| `max_open_suspects` | integer, 1 to 100 | `25` | Open suspects at once before the queue escalates |

### Admission and landing

- **Admission.** A PR's required checks must pass on a test ref built from a pinned head on a base that was the tip within `max_base_age`. The trust, approval and condition gates are the strict ones.
- **Landing.** Before landing, OpenMerge rereads the PR and fails the attempt unless the head is still the pinned head, the trust gate passes freshly (approvals, permissions, fork rules), the conditions still hold, and the admission base is still within `max_base_age`. It then composes the pinned head onto the current tip with the configured merge method, giving the landing commit L, and lands L with the same atomic push leased on the base and the fence. A composition conflict is not landed (see Conflicts).
- **Durable record before the push.** The start event records the attempt ID, the pinned heads, the tested commit T, the base T was tested on, the composed L, and the queue's configuration generation. In post-merge queues the fence commit records both, as `land/<attempt> tested=<T> landed=<L>`. Fence recovery reads both fields, so a crash between push and record can never present L as tested or lose which commit landed.
- **Honest reporting.** The PR's check, the landed comment, status and API all say "tested as `T` on `<base>`, landed as `L`". They never describe L as tested.

### Verifying main

**Every landed commit is checked.** CI already runs on pushes to the base. Each required check on each landed commit ends as passed or failed, where failed means it failed twice: once, then once more after one automatic rerun to filter flakes. A failure that an open suspect already explains is not rerun.

**Suspects, not a red base.** At agent scale the base is rarely free of every bug at once: with 300 bad changes a day and about an hour to detect and remove each, a dozen can be live in the tip. So OpenMerge tracks each defect separately. A suspect is a landed commit S with a set of checks K that passed on the nearest earlier commit with a known result for them and failed on S. If CI skipped or cancelled runs between that commit and S, for example through concurrency groups, OpenMerge runs CI on each skipped commit in parallel by pushing it to `openmerge/verify/<base>/<sha>`, and does not binary-search, because history need not be monotonic. If those results cannot be obtained, the suspect is inconclusive. A later failure of a check in K is explained by the earliest unremedied suspect with that check in the commit's history; an unexplained failure makes a new suspect. A suspect never explains failures on its own remedy, and an explained failure never counts as verifying anything.

**Remedies.** With `on_red: revert`, OpenMerge composes a revert of S onto the tip and lands it ahead of ordinary PRs. The revert carries `Reverts:` and credits S's author. Generated reverts skip admission testing so a remedy is not delayed by a full CI run; they still take the fenced landing path and every post-merge check. A revert that breaks later commits becomes a suspect itself and, as a remedy, escalates instead of being reverted. A revert is verified when the checks in K pass on the revert commit, or on the first later commit with known results for K. With `pause`, ordinary landings pause while S is open. With `notify`, OpenMerge only reports.

**Residual failures.** Two independent defects can break the same check: T lands after S, and its failures look explained by S. If checks in K still fail on S's revert, OpenMerge keeps the revert, since S did fail those checks against its predecessor, and searches for the residual cause. Suspects found this way form a chain that starts at S. Each search composes every landed commit after S, up to the newest remedy, with every suspect already in the chain reverted, pushes each to a verify ref, and runs them in parallel, up to 50 commits. Each search first runs a baseline: the checks must pass on S's predecessor under the same reverts, so the search starts from a known-good composition. The first probe that still fails those checks is the next suspect in the chain, and the previous one is marked remedied. A probe whose reverts conflict when composed has no result. A chain gets at most three residual searches, so it costs at most 153 CI runs. If a search finds no failing probe, its baseline does not pass, the range exceeds 50 commits, any probe has no result, or the chain is exhausted, the chain escalates. At high landing rates the range between a suspect and its revert usually exceeds 50 commits, so defects that share a check usually escalate; splitting broad aggregate checks into narrower ones (and Phase 4 scopes) is what keeps them automatic.

**Bounded automation.** Each suspect gets at most one automatic remedy. OpenMerge never automatically reverts a commit it authored as a remedy, and never retries a remedy that conflicts. A suspect escalates when it is inconclusive, its remedy conflicts or is not verified, or it stays open longer than `max_red`, counted from when its failure was confirmed. The queue also escalates when more than `max_open_suspects` are open at once. Escalation pauses ordinary landings while culprit finding, remedies for other suspects, and verification keep running, and gives a human the evidence. A human fixes the base by landing a fix through the queue or pushing it directly. A writer's `@openmerge refresh` asks for verification again. Ordinary landings resume automatically once no escalated suspect remains open, never on the command alone.

**Report.** The suspect PR's author gets one structured comment and a machine-readable event: the commit, the failed checks, the evidence, and the remedy's SHA or the reason it escalated.

**Verification frontier.** OpenMerge tracks, per base, the newest landed commit F such that every landed commit up to and including F has known results for every required check. A commit with pending checks stops the frontier, even if later commits already passed.

**Green ref.** `openmerge/green/<base>` is a protected, App-only ref that deploys follow instead of the base. It always points at a commit on which every required check passed, and it comes from one of two places:

1. *A base commit.* The newest commit C at or behind the frontier whose required checks all passed, with no open suspect at or before C.
2. *A deploy candidate.* While open suspects block the first option, OpenMerge composes a candidate D from the frontier commit F with a revert of every open suspect at or before F whose revert is not already in F's history, pushes it to `openmerge/verify/<base>/candidate-<n>`, and moves the green ref to D if every required check passes on D. It builds at most one candidate at a time and starts the next when the previous result arrives. If the reverts conflict when composed, or D fails a check, the green ref stays where it is, the evidence is reported, and the next candidate waits until F or the set of open suspects changes.

The green ref's base commit (C, or the F a candidate was built from) only moves forward along the base's first-parent history. Candidates are not ancestors of each other, so moves between them are not fast-forwards; deploy tooling must fetch the green ref by name, not expect fast-forward updates. Each move is a compare-and-swap, and the event records the base commit and, for a candidate, the suspects it reverts. The promise is that deploys only receive a commit that passed every required check on that exact commit. It is not a promise that those checks catch every bug.

### Conflicts

A composition conflict produces a structured event naming the conflicting files and the base commit, and the PR returns to its author. Until change identity exists (Phase 3) the fixed push re-enters at the back of the queue. With change identity, the PR may hold its position for `conflict_grace`: one deadline set at the first conflict and never renewed, a bounded number of conflict retries, fresh checks and trust for each new head, and PRs behind it still land while it waits.

### Switching modes

A mode change is a new configuration generation. Admission under the old generation freezes. In-flight consequential actions finish or are fenced as today. Outstanding post-merge obligations (open suspects, pending reruns, verify runs, candidates, remedies) and any escalation pause belong to the queue, not to a mode, so they survive the switch and keep running under the generation that created them. The new mode admits work only after no suspect is open and no escalation pause remains. Recovery evidence is tied to the persisted attempt's generation, not to the queue's current mode: the fence parser accepts both fence forms, then checks that the form matches the attempt it names, so switching modes can never hide an in-flight landing.

## Consequences

- Product principle 1 becomes a promise about honesty: strict queues land exactly what was tested; post-merge queues say exactly what was tested before landing and keep a green ref that only passing code reaches.
- Post-merge throughput is bounded by landing latency and conflict rate, not by CI duration. The base can be red between a bad landing and its remedy, so deploys must follow the green ref. Setup and the rules checks verify CI on base pushes and on `openmerge/verify/**`, and that `openmerge/green/*` and `openmerge/verify/*` are protected.
- CI cost per merged PR is about two runs (admission and post-merge), plus reruns, verify runs for skipped commits, one deploy candidate at a time, and up to 153 runs per residual-search chain. The simulator must model a finite runner pool so its exit tests measure latency under a CI budget, not with unlimited CI.
- Post-merge landings use new event types and a new fence-commit form. Existing event types and the strict fence form stay byte-for-byte unchanged ([ADR 0002](0002-storage-and-state-model.md)); the shared fence parser accepts both forms and validates each against its persisted attempt.
- The simulator's invariants for post-merge queues change from "main is always green" to: the green ref only points at a commit on which every required check passed, and its base commit never moves backward; every suspect is remedied and verified or escalates; and no landing is reported as tested unless it was.
- [ADR 0004](0004-forge-driver-interface.md)'s "what lands is byte-for-byte what was tested" holds for strict queues only. The landing mechanism, one atomic push leased on base and fence, is unchanged in both modes.

## Alternatives considered

- **Strict only, with more speculation.** Speculation multiplies CI cost to buy throughput and still serializes on landing a tested commit. It stays the default for most teams, but it cannot reach agent-scale volume, and teams at that volume will route around a tool that refuses them.
- **Post-merge only.** Many teams cannot accept a red base even briefly, or deploy from the base directly. Removing strict mode would give up the guarantee the project started from.
- **Optimistic batching (Zuul's HEADGREEN) instead.** It tolerates rare intermittent failures but keeps CI-bound serialization, so it does not answer the scale problem. It remains a Phase 2 option within strict mode.
- **Leave culprit finding and reverts to external tooling.** Culprit finding needs the queue's own record of what landed when and what was tested where. Splitting it out loses that record and keeps the base red longer.
- **Automatic revert-of-revert or repeated remedies.** Each automatic step can create the next failure. One verified remedy per suspect, then a human, keeps automation from making things worse.
- **Require a fully green base before the green ref moves.** At agent-scale defect rates no base commit is ever free of every live defect, so the green ref would freeze. Deploy candidates give a tested commit to deploy without waiting for the base to be clean.
