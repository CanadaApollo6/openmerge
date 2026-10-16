# 0005. Offer post-merge verification as a per-queue mode alongside strict landing

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

OpenMerge's first principle is "what you tested is what you merged": a queue lands only the exact commit CI tested ([ADR 0004](0004-forge-driver-interface.md)). That guarantee costs throughput. Each landing must wait for CI on a commit built on the current base, so a queue serializes on CI time. Our simulator shows the ceiling: at 2,000 PRs a day with 30-minute CI, the strict serial queue's median time to merge was 6.7 days. Phase 2 speculation raises the ceiling but does not remove it.

Agent-scale users have already left this model. Anthropic reports growing from about 2,500 to over 30,000 merges a day between May and September 2026, and writes that "a proper merge queue, for example, doesn't make much sense at agent scale" and asks "what does CI even mean when hundreds or even thousands of changes can merge while tests are running?" Instead it invests in "post-merge and pre-deploy automated testing", automated bisect and revert pipelines, and agents that fix conflicts as they appear ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control), 2026-10-13; see also [landscape research](../research/landscape.md)). Chromium sheriffing and Google TAP culprit finding are older versions of the same idea.

The roadmap listed "post-merge verify + auto-revert" as a Phase 6 gating mode. The teams who need it are the teams agents are creating now.

## Decision

**Verification is a per-queue setting with two modes.** `verification: strict` is the default and keeps today's behavior exactly. `verification: post_merge` is opt-in. A queue's mode is part of its configuration and is reported in every status and API response. Switching a queue between modes takes effect only when it has no batch in testing or landing.

**Strict mode is unchanged.** The landed commit is byte-for-byte the tested commit. Speculative batching in Phase 2 applies to this mode.

**Post-merge mode separates admission testing from landing.**

- *Pre-merge check.* A PR is admitted once its required checks pass on a test ref built from the PR on a base no older than `max_base_age` (a duration and a commit count, whichever is reached first). Trust, approval and condition gates are the same as in strict mode.
- *Landing.* OpenMerge composes the PR onto the current base tip with the configured merge method and lands it with the same atomic push leased on the base and the fence. Landing stays serialized per base, but it costs seconds, not a CI run. If the composition conflicts, the PR returns to its author (see conflicts below) instead of landing.
- *Honest record.* The landing event records the commit that was tested, the base it was tested on, and the commit that landed. Status, API, the PR's check and the landed comment all say "tested on `<base>`, landed as `<sha>`", never that the landed commit itself was tested.

**Main is verified after landing, and deploys follow a green pointer.** CI runs on every landed commit as it already does for pushes to the base. OpenMerge maintains a protected ref, `openmerge/green/<base>`, and advances it with a compare-and-swap to the newest landed commit whose required checks passed and whose predecessors since the previous green commit are either passing or reverted. Deployments and release tooling consume the green ref, not the base branch. The green ref is fenced like the landing fence: only the App can move it, and it only moves forward along the base's first-parent history.

**A red commit triggers culprit finding, then the configured remedy.**

1. Failed checks on a landed commit are rerun once, to filter flakes.
2. If they fail again, OpenMerge bisects the range between the green commit and the red commit. It tests intermediate landed commits by pushing them to `openmerge/verify/<base>/<sha>` refs; the commits already exist, so no new composition is involved.
3. The culprit is the first failing commit. With `on_red: revert` (the post-merge default), OpenMerge composes a revert of the culprit and lands it through the same queue ahead of other PRs; the revert carries `Reverts:` and links to the culprit PR. With `on_red: pause`, the queue pauses new landings. With `on_red: notify`, it only reports.
4. The culprit PR's author gets one structured comment and a machine-readable event: which commit, which checks, the bisect evidence, and the revert's SHA.
5. If the base stays red for longer than `max_red` (a duration and a commit count) despite remedies, the queue pauses landings and says why. A pause never stops culprit finding or reverts.

All of this is recorded as events and folded by the pure planner, like everything else ([ADR 0002](0002-storage-and-state-model.md)). Bisect is the same binary search the strict queue already uses for failed batches, applied to landed commits.

**Conflicts go back to the author, not to the end of the line.** In both modes, a composition conflict produces a structured event naming the conflicting files and the base commit. The PR keeps its queue position for `conflict_grace`. If its author (often an agent) pushes a fix within that time, the fixed head re-enters at the same position after its checks pass. Otherwise the PR is dequeued as today. Re-entry depends on change identity (Phase 3); until then, a fixed push re-enters at the back.

## Consequences

- Product principle 1 becomes a promise about honesty, not about one guarantee: strict queues land exactly what was tested; post-merge queues say exactly what was tested before landing, keep a green ref that only passing code reaches, and never present an untested commit as tested.
- Post-merge throughput is bounded by landing latency and conflict rate, not CI duration. The base branch can be red between a bad landing and its revert; the green ref, not the base, is the safe thing to deploy.
- Teams that adopt post-merge mode must point deploys at the green ref and must have CI on pushes to the base. The setup and rules checks verify that CI runs on `openmerge/verify/**` and that the green ref is protected.
- New refs (`openmerge/green/*`, `openmerge/verify/*`) join the ruleset setup, the rules validator, and the fence-style protection rules.
- New event types are additive under wire v1 ([ADR 0002](0002-storage-and-state-model.md)). The simulator gains a post-merge model with injected bad landings and flakes; its invariants change from "main is always green" to "the green ref is always green, every red commit is reverted or explained, and no landing is reported as tested unless it was."
- [ADR 0004](0004-forge-driver-interface.md)'s "what lands is byte-for-byte what was tested" holds for strict queues only. The landing mechanism itself (one atomic push leased on base and fence) is unchanged in both modes.
- Reverts are commits OpenMerge authors. They use the App committer identity, credit the culprit's author, and go through the same trust and landing path as any other change.

## Alternatives considered

- **Strict only, with more speculation.** Speculation multiplies CI cost to buy throughput and still serializes on landing a tested commit. It remains the right default for most teams, but it cannot reach agent-scale volume, and teams at that volume will route around a tool that refuses them.
- **Post-merge only.** Many teams cannot accept a red base even briefly, or deploy from the base branch directly. Removing strict mode would give up the guarantee that made the project worth building.
- **Optimistic batching (Zuul's HEADGREEN) instead of post-merge mode.** It keeps pre-merge testing but tolerates rare intermittent failures. It does not remove the CI-bound serialization, so it does not answer the scale problem. It stays a Phase 2 option within strict mode.
- **Leave reverts and bisect to external tooling.** Culprit finding needs the queue's own record of what landed when and what was tested where. Splitting it out loses that record and leaves the base red longer.
