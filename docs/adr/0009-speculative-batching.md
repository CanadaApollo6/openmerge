# 0009. Test a speculative chain of batches with an adaptive window

- **Status:** Proposed
- **Date:** 2026-10-17

## Context

Today a queue has one active batch ([ADR 0002](0002-storage-and-state-model.md), [ADR 0004](0004-forge-driver-interface.md)). The planner forms up to `batch.max_size` changes into a test commit on the current base, waits for CI, lands that exact commit with one atomic push leased on the base and the fence, and only then starts the next batch. A failed batch splits into two halves that are tested in turn.

Throughput is therefore bounded by CI duration: one batch per CI run. The simulator shows the ceiling. At 2,000 PRs a day with 30-minute CI, batches of up to eight still had a median time to merge of 6.7 days ([ADR 0005](0005-verification-modes.md)). The first work adopters see the same effect at smaller scale. A repository with 10 to 20 PRs a day and 30 to 40 minute CI forms a queue whenever two PRs are ready within one CI run of each other. A repository with very slow CI waits hours for even a few PRs.

The Phase 2 exit test is: at 2,000 simulated PRs a day with 30-minute CI, median time to merge stays under twice the CI duration, and CI runs per merged PR stay under 1.5. Zuul's dependent pipelines reach this kind of throughput by testing batches speculatively on top of each other, with a window that adapts to failures. This ADR adopts that model within strict verification, so what lands is still byte-for-byte what was tested.

## Decision

### A chain of batches

**A queue may test several batches at once, each built on the one before it.** Batch 1 is built on the base tip. Batch 2 is built on batch 1's tested commit, so its test commit contains batch 1's changes and its own. Batch *n* is built on batch *n−1*'s tested commit. Each batch has its own test ref (`openmerge/queue/<base>/<batch-id>`), its own CI, and its own result. The chain is ordered, and every batch's position is recorded when it starts.

**Batches land strictly in chain order, through the existing landing path.** The head of the chain lands exactly as a single batch does today: one atomic push leased on the base and the fence, moving the base to its tested commit. Batch *n* may start landing only once batch *n−1* has landed, so the base is exactly the commit batch *n* was built on. Its landing push is leased on that commit and lands batch *n*'s tested commit, which is exactly what CI tested. Landing attempts on one base remain serialized by the fence, and settlement still runs before anything else ([ADR 0006](0006-multi-repository-serving.md)). Nothing about the landing protocol, the fence format, or recovery changes.

**Fast-forward queues chain only linear history.** With `merge_method: fast-forward`, a batch may follow another only if its first head strictly descends from the previous batch's last head, as consecutive heads within a batch must today. Each batch's marker commit sits above its own last head, and landing moves the base to the heads, not the markers.

### The window adapts to failures

**The number of batches testing at once is the window, and it adapts.** Each queue keeps a window *W* between `speculation.min_depth` and `speculation.max_depth`. The window grows by one each time a batch lands, and halves (rounding down, never below the minimum) each time a batch fails for a reason other than a flake that later clears. This is additive-increase, multiplicative-decrease, as in Zuul: a run of green batches earns more speculation, and a failure cuts the CI that a broken change can waste. The planner computes *W* from the queue's recorded history, so it stays a pure function of the event log ([ADR 0002](0002-storage-and-state-model.md)). No new event records it.

**Batch size adapts to the observed failure rate.** `batch.max_size` stays the ceiling. The planner estimates each change's chance of failing from the queue's last 50 batch outcomes. It then uses the largest size up to the ceiling whose predicted pass probability, `(1 − p)^size`, stays at or above `batch.target_pass_rate` (default 0.8). A queue with a clean history batches to its ceiling; a queue that keeps failing shrinks its batches until most pass. The estimate is computed from recorded outcomes, so replay reaches the same sizes.

**Each new batch also waits for enough work.** The existing `batch.max_wait` still applies to the chain's newest batch: a partial batch waits for more changes until its oldest member has waited that long. With an empty queue behind it, a chain simply stops growing.

### Failures

**A failed batch invalidates everything built on it.** When a batch fails, every later batch in the chain is cancelled: their test commits contain the failed batch's changes, so their results say nothing about the remaining changes on their own. Their CI runs are cancelled where the forge supports it, and their changes return to the queue in their original order. The failed batch is then split and its halves tested, as today, ahead of fresh work. The window halves.

**Optimistic failure resolution is opt-in.** With `speculation.on_failure: wait_for_successor`, a failed batch with a successor still testing is held instead of split. If the successor passes, its tested commit contains the failed batch's changes too, and every required check passed on exactly that commit. The successor then lands, built on the base the failed batch was built on, and both batches' changes land together as one tested commit. The failed batch's result is recorded as superseded, not as passed, and nothing reports it as having passed on its own. If the successor fails as well, or no successor is testing, the failed batch is split as above. The default is `split`, so a failure is always investigated rather than outvoted; teams with known flaky suites can opt in. This keeps principle 1, because what lands is still exactly what was tested, but it does change which commit history the base receives, and status says so.

**Changes leaving the queue invalidate from their position.** A change that leaves the queue (a new head, closing, dequeuing, a trust or condition change) invalidates the batch that contains it and every batch after it, which are rebuilt without it. Batches before it are unaffected. A base move OpenMerge did not make, a configuration digest change, or a change to the queue's approval inputs ([ADR 0007](0007-changes-not-prs.md)) invalidates the whole chain, as it invalidates the single batch today.

### Bounding CI cost

Speculation trades CI runs for latency. The bounds are explicit:

- `speculation.max_depth` caps how many batches test at once. Its default is 1, which is today's behavior exactly, until the exit test below passes and the project has run the new default on its own repository. After that, a release note can change the default to a small adaptive window (for example, minimum 1 and maximum 4); queues that set `max_depth` keep their setting.
- The window halves on every failure, so a burst of broken changes quickly returns the queue to near-serial testing.
- Cancelled batches are cancelled in CI where the forge allows, and status reports the CI runs each merged change cost, including wasted runs and why they were wasted.

### Interaction with other decisions

- **Verification modes.** Speculation applies to strict queues only. Post-merge queues ([ADR 0005](0005-verification-modes.md)) compose at landing and already decouple throughput from CI.
- **Priorities.** Higher-priority changes enter the chain at the first batch that has not started yet. Preempting batches that already started is a separate decision in the priorities work, not part of this one.
- **Approvals.** A change waiting for approval is skipped by batch formation, and speculation that depends on a rejected composition is discarded, as ADR 0007 already specifies.
- **Multiple bases.** Each base has its own chain, fence and window ([ADR 0006](0006-multi-repository-serving.md)).

### Storage

**Single-batch queues write exactly what they write today.** A queue with `max_depth: 1` and `on_failure: split` keeps emitting only the existing event and action types, so its log replays byte-for-byte as before and its golden fixtures do not change. Chains need to record a batch's parent and position, and a superseded result. They use new, additive event and action types with golden fixtures, never new fields on existing ones. As with every addition under ADR 0002, an older binary cannot replay a log that contains them, so rolling back past their first use needs a database restore.

### Simulation and exit test

The simulator gains chains, the adaptive window and size, CI cancellation, a finite pool of CI runners, and injected failures: independent bad changes, interacting changes, and flaky checks that pass on rerun. Its invariants stay those of strict mode: every landed commit is exactly a tested commit, the base only moves forward, and no change lands without passing checks on the commit that contains it. The Phase 2 exit test is the one in the roadmap: at 2,000 PRs a day with 30-minute CI, median time to merge under twice the CI duration and CI runs per merged PR under 1.5. It is extended with two smaller profiles matching the first adopters: 20 PRs a day with 30-minute CI, and 5 PRs a day with 3-hour CI. For each, the report gives the median and 95th-percentile time to merge, compared with depth 1.

### Visibility

Status and the API show each batch's position in the chain, the batch it was built on, the current window and batch size with the reason for each, and why any batch was cancelled. A change's status says which batch it is in and how many batches are ahead of it.

## Consequences

- Throughput is no longer bounded by one batch per CI run. Time to merge for a change queued behind others approaches one CI run plus queueing, not one CI run per batch ahead of it.
- CI cost rises with the window, and falls back when failures cut it. Teams control the ceiling with `max_depth`, and the default does not change until the simulator and dogfooding justify it.
- Landing safety does not change: one batch lands at a time, through the same fence, onto exactly the commit it was built on.
- The planner keeps more state: the chain, and outcome history for the window and size estimates. Both are derived from the log, so the planner stays pure and replay stays deterministic.
- New event and action types arrive for chains and superseded results. Single-batch queues never write them.
- Optimistic resolution is available but off by default, so the default behavior never lands a batch whose own run failed.

## Alternatives considered

- **More parallel independent batches** (each built on the base, Mergify-style parallel checks). Each batch must land on a base that has since moved, so its result is stale by the time it can land. That breaks strict verification or forces retesting.
- **A fixed speculation depth.** Simpler, but a fixed depth either wastes CI during failure bursts or leaves throughput unused in quiet periods. The adaptive window costs little extra and degrades to serial testing when it should.
- **Predictive scheduling** (Uber's SubmitQueue): choose which speculations to run from predicted pass probabilities and build times. It can beat a window, but it needs models trained on history the queue does not have yet. It stays in Phase 6, and the outcome history recorded here is its input.
- **Optimistic resolution by default.** Zuul's HEADGREEN behavior raises throughput when suites are flaky, but it means a batch whose own CI failed can land, carried by its successor. OpenMerge offers it, and leaves the default to investigate failures.
