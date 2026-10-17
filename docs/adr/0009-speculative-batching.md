# 0009. Test a speculative chain of batches with an adaptive window

- **Status:** Accepted
- **Date:** 2026-10-17

## Context

Today a queue has one active batch ([ADR 0002](0002-storage-and-state-model.md), [ADR 0004](0004-forge-driver-interface.md)). The planner forms up to `batch.max_size` changes into a test commit on the current base, waits for CI, lands with one atomic push leased on the base and the fence, and only then starts the next batch. A failed batch splits into two halves that are tested in turn.

Throughput is therefore bounded by one batch per CI run. At 2,000 PRs a day with 30-minute CI, batches of up to eight have a median time to merge of 6.7 days in the simulator ([ADR 0005](0005-verification-modes.md)). The first adopters see the same effect at smaller scale: a repository with 10 to 20 PRs a day and 30 to 40 minute CI queues whenever two PRs are ready within one CI run, and a repository with very slow CI waits hours for even a few PRs.

Zuul's dependent pipelines raise throughput by testing batches speculatively on top of each other, with a window that adapts to failures. This ADR adopts that model within strict verification.

It is also honest about the limit. A change in the *k*-th speculative batch keeps its result only if the *k − 1* batches ahead of it pass, and every broken change costs several CI durations of nearly serial resolution: confirming the failure, then bisecting. Review modelling of these rules puts strict capacity at a few hundred changes a day when 2% of changes are broken, and around 1,400 a day at 0.2%, almost independent of depth. Strict speculation therefore helps most at the volumes and defect rates of the first adopters. At high volume with realistic defect rates, [ADR 0005](0005-verification-modes.md)'s post-merge mode is the answer, not more speculation, and the Phase 2 roadmap target of 2,000 changes a day is superseded (see Simulation and exit test).

## Decision

### A chain of batches

**A queue may test several batches at once, each built on the one before it.** Every batch records two commits: its *tested* commit, which CI runs on, and its *landing* commit, which the base will move to. They are the same commit for `squash`, `merge` and `rebase`. For `fast-forward`, the tested commit is the marker above the batch's last head, and the landing commit is that last head ([ADR 0004](0004-forge-driver-interface.md)). Batch 1 is built on the base tip. Batch *n* is built on batch *n−1*'s landing commit, so its tested commit contains every change ahead of it. Each batch has its own test ref (`openmerge/queue/<base>/<batch-id>`), checks, checks generation, refresh counters and timeout origin. CI results stay bound to the exact tested commit, test ref and accepted producer, under the existing provenance rules.

**Fast-forward queues rarely speculate.** A fast-forward batch can follow another only if its first head strictly descends from the previous batch's last head. That is uncommon in practice, so fast-forward queues mostly keep testing one batch at a time.

**Batches land strictly in chain order, through the unchanged landing path.** A batch may start landing only when every batch ahead of it has landed and settled through the fence, and the settled base is exactly its predecessor's landing commit. Its push is the existing one: atomic, leased on that commit and on the fence, moving the base to its landing commit. For fast-forward, the marker's parent and tree are verified exactly as today. An unsettled predecessor blocks every batch behind it. An abandoned predecessor invalidates every batch behind it, and those changes are rebuilt with new batch IDs. A base move OpenMerge did not make invalidates the whole chain. An observation of the base that matches one of OpenMerge's own in-flight pushes is reconciled through that attempt, never treated as an outside move. Once a batch's `LandingStarted` is durable, nothing discards it, and it settles through the fence as always ([ADR 0006](0006-multi-repository-serving.md)).

**Checks stay revocable per batch.** Each batch keeps its own checks generation, and both durable start gates reject a landing for an old generation, even after a delayed pass. Reopening a batch's checks revokes its landing eligibility and holds every batch behind it; any failure that follows is handled by the blame rules below.

### Blame belongs to the oldest batch

**Only the oldest unlanded batch can be blamed.** When a batch's failure is confirmed (see below) and every batch ahead of it has landed and settled, its changes are diagnosed as today. A multi-change batch splits into halves, and a failing single change is dequeued with its reason. A failure in a later batch is provisional, because its tested commit also contains every change ahead of it. It stays recorded but blames no one. If the batches ahead of it land, it becomes the oldest and its confirmed failure is diagnosed then. If one of them is invalidated, the provisional result is discarded without blaming its members. A build conflict follows the same rule.

**A confirmed failure cancels everything behind it.** When the oldest batch's failure is confirmed, every later batch is invalidated: their tested commits contain the failing changes. Their changes return to the queue in their original order, and their CI runs are cancelled where the forge supports it (see Bounding CI cost).

**Bisection runs ahead of fresh work.** The halves of a split batch become the head of a new chain, with the second half built on the first half's landing commit, within the window. Fresh changes do not form batches behind unresolved halves. One half almost certainly contains the failing change, so fresh work built on it would mostly be wasted. This costs fresh changes up to a few CI runs of latency after a failure, and the simulator reports that cost.

### Confirmed failures

**In speculative queues, a batch's failure is confirmed only after one same-commit rerun.** When a required check fails on the oldest batch of a queue with `max_depth` above 1, OpenMerge reruns the failed checks once on the same tested commit before treating the batch as failed, as [ADR 0005](0005-verification-modes.md) does for post-merge verification. A provisional failure in a later batch is not rerun until that batch becomes the oldest, so one broken change does not trigger reruns across the whole window. A batch that passes on the rerun is a pass, and the flaky run is recorded. Queues at depth 1 keep today's behavior, in which a failure splits the batch immediately, unless they opt into retries.

This is the Phase 2 bounded-retry work, which is implemented before the chain. It records the original run and the rerun by identity, and the rerun request once. Duplicate observations of a run neither consume the retry nor confirm a failure. Reopening checks does not restore the retry budget. A confirmation, once recorded for a batch's checks generation, is final. The original timeout deadline governs the original run, and the rerun gets its own deadline of one check timeout from its request; both are recorded, so reopening checks cannot extend either. When `check_timeout: 0` disables the original timer, the rerun still gets the default one-hour deadline from its request. An unconfirmed dispatch times out with “rerun dispatch never confirmed”. The existing qualifying-refresh rule still applies, and a batch waiting to land after passing never times out. Forges or CI providers that cannot rerun a check are reported as such, and their failures count as confirmed on first failure. A check timeout is a confirmed failure for blame, as today.

For implementation step 2, before chains exist, `checks.retry_failed: 1` on a
queue opts into this behavior; only `0` and `1` are valid and the default is `0`.
`Queue.QueueConfig` maps this key to the effective `ChecksRetryFailed` policy
used by both planner and executor; step 3 can enable retries for `max_depth > 1`
by changing that one assignment. The planner's `retryOldestBatch` hook will
restrict requests to the oldest batch. Later provisional failures must wait for
that policy decision.

Retry-enabled queues write additive `checks_observed` records with the accepted
producer, run ID and attempt, and the original deadline. `check_rerun_requested`
pins the failed identities, request time and rerun deadline before the optional
forge call. `check_rerun_dispatch_progress` persists each Actions workflow's
pre-POST intent and subsequent acceptance. Ambiguous outcomes reconcile from
an increased workflow `run_attempt` without repeating the POST; an inconsistent
non-completed status at the original attempt is deferred. Definite non-dispatch
(local refusal before sending or server rate-limit rejection) is recorded
separately and permits a later fenced intent after cooldown; `check_rerun_dispatched`
records aggregate support or an unsupported reason. Only GitHub Actions check
runs are supported in this implementation.
`checks_failure_confirmed` makes blame durable. The corresponding new actions
are `record_check_rerun`, `rerun_checks` and `confirm_check_failure`. Pending
observations still require a qualifying `checks_refreshed` read to time out.
Both timer origins and the bounded retry state belong to the batch projection;
snapshot compaction must preserve them. Existing `checks_completed` records
retain their immediate pass/failure meaning for legacy depth-1 batches. An
aggregate failure on a chained batch is provisional; at the head it requires a
retry-aware refresh and the same-commit rerun path before blame (unsupported
forges excepted). A later aggregate pass cannot resolve a recorded provisional
failure: it lacks the attempt identities required to prove a rerun passed. Only
a qualifying identified observation resolves the marker; until then, landing is
blocked and the recorded retry/timeout path remains active.
Older binaries cannot replay these additive types: rollback after
first use requires a compatible database restore.

### The window

**The window bounds every admitted, unlanded batch.** A queue keeps a window *W* between `speculation.min_depth` and `speculation.max_depth` (`1 ≤ min_depth ≤ max_depth ≤ 32`; `min_depth` defaults to 1, and `max_depth` defaults to 1). Batches that are building, testing, passed and waiting to land, or landing all count against it. A passing batch does not free its slot until it lands. A new batch is admitted only while fewer than *W* batches are open.

**The window grows on landings and halves on confirmed failures.** *W* starts at `max_depth` for a new queue, as Zuul's window starts high, so it speculates immediately; confirmed failures bring it down toward `min_depth`. Becoming idle does not reset *W*: an existing queue keeps the window its history gives it. A configuration change that alters `min_depth` or `max_depth` clamps *W* into the new bounds rather than resetting it. Each landing, including the landing of a bisection half, increases *W* by one, up to `max_depth`. Each confirmed checks failure of an original (not bisection) batch halves *W* once, rounding down and never below `min_depth`. Failures found while bisecting that batch, including timeouts, do not halve it again. A check timeout of an original batch is a confirmed failure and halves *W* once, which also eases load when the window itself is overloading CI. Build conflicts do not measure CI health and do not change *W*. Provisional failures, invalidated batches, cancellations and duplicate observations change nothing. A shrinking window never cancels batches already admitted; it only stops new ones until enough have landed. This is additive-increase, multiplicative-decrease, as in Zuul. *W* is computed from the recorded history, so the planner stays a pure function of the log ([ADR 0002](0002-storage-and-state-model.md)). The simulator reports the window's equilibrium; it is not assumed to be optimal.

### Batch size

**Adaptive batch size is opt-in.** Setting `batch.target_pass_rate` turns it on; without it, batches form exactly as today, so existing queues keep today's behavior. With it, the planner estimates *p̂*, the observed rate at which changes are dequeued for a confirmed checks failure or timeout. *p̂* is the number of such dequeues divided by the number of changes that left the queue either by landing or by such a dequeue, over the most recent 200 such changes in event-log order. Conflict dequeues are excluded, since they say nothing about checks. At low volume the history is old (200 changes is about 40 days at 5 a day), which is acceptable for a size ceiling. The batch size is the largest *k* up to `batch.max_size` for which `(1 − p̂)^k ≥ target_pass_rate`, and never less than 1. `target_pass_rate` must be strictly between 0 and 1. With fewer than 20 changes in the history, the size is `batch.max_size`. The history restarts when the queue's configuration digest changes. Pending bisection groups keep their sizes; only fresh batches use the estimate. This assumes changes fail independently. That is a heuristic: interacting changes break it, and the status shows the inputs so operators can see when it misleads.

`batch.max_wait` still applies to the chain's newest batch: a partial batch waits for more changes until its oldest member has waited that long.

### Interaction with other decisions

- **Verification modes.** Speculation applies to strict queues only. Post-merge queues ([ADR 0005](0005-verification-modes.md)) compose at landing.
- **Approvals.** Approvals gate landing, not batch formation ([ADR 0007](0007-changes-not-prs.md)). A landing-gate rejection invalidates that batch and every batch behind it; earlier batches are unaffected. Changes to governing policy or classification inputs invalidate every batch that has not started landing. Review, receipt and intent observations update the affected change's gate as ADR 0007 specifies.
- **Changes leaving the queue.** A new head, closing, dequeuing, or a trust or condition change invalidates the batch containing the change and every batch behind it.
- **Configuration.** A configuration digest change invalidates every batch that has not started landing, as it does for the single batch today, so changing `max_depth` takes effect on a fresh chain.
- **Priorities.** Higher-priority changes enter at the next eligible fresh-batch admission, after any unresolved bisection. Preempting started batches is left to the priorities work.
- **Multiple bases.** Each base has its own chain, fence and window ([ADR 0006](0006-multi-repository-serving.md)).

### Bounding CI cost

- `max_depth` caps open batches. Its default of 1 is today's behavior.
- **Cancellation is a new, optional forge capability** ([ADR 0004](0004-forge-driver-interface.md)). It targets the identified runs for an invalidated batch, is idempotent and best effort, and never authorizes a landing or proves that a run stopped. Status distinguishes requested, confirmed and unsupported cancellations. On GitHub Actions it needs the App's `actions: write` permission, which setup adds. Where cancellation is unsupported, invalidated runs complete, keep using runners, and count as wasted.
- Late results from invalidated batches never revive them, and their test refs are cleaned up like any other.

### Storage

**Encoding compatibility and behavioral compatibility are both kept by default.** A queue with `max_depth: 1`, no `target_pass_rate` and no retries behaves exactly as today and writes only the existing event and action types, so its log replays byte-for-byte and its golden fixtures do not change. Chains add new, additive event and action types with golden fixtures: a chained batch start that records the parent batch, the tested and landing commits and the chain position, and an invalidation that records which batches it discards and why. Retries add their own types for the rerun request and its identified runs, which a depth-1 queue writes only if it opts in. Records written before retries existed keep their original meaning on replay: an existing failed `ChecksCompleted` still splits or dequeues as it did. A batch started by an existing `BatchStarted` is interpreted as a chain of one. Snapshots ([ADR 0002](0002-storage-and-state-model.md)) keep compact state, not unbounded history: the chain, each batch's checks and retry state, pending bisection groups, the current window, and the last-200 outcome ring that the batch size derives from. Replay from a snapshot then reaches the same decisions. As with every addition under ADR 0002, an older binary cannot replay a log containing the new types, and returning to `max_depth: 1` does not change that: rolling back past their first use needs a database restore.

### Simulation and exit test

The simulator gains chains, the window and adaptive size, a finite pool of CI runners (one runner per run) with cancellation modeled as supported or unsupported, same-commit reruns of the failed checks taking a full CI duration, and three kinds of injected failure: independent broken changes, interacting changes, and flaky runs. It counts every run started, including cancelled runs and reruns, and reports runner-minutes per merged change alongside runs per merged change. Its invariants are those of strict mode: every landed commit is exactly a tested commit (or, for fast-forward, the verified head beneath one), the base only moves forward, and no change lands without passing checks on a commit that contains it.

**Phase 2's strict exit test is the adopter profiles.** Each runs on seeds 1 through 8, each seed simulating 30 days of the simulator's existing uniform arrival pattern, with fixed CI durations, a 1% chance per run of a flaky failure, 2% independent broken changes, `batch.max_size: 8`, `batch.max_wait: 5m`, a check timeout of 1.5 times the CI duration, and 50 runners. Results are pooled across seeds. Each profile must pass with cancellation supported and, separately, with it unsupported. The first day of each seed is also reported on its own as the cold-start result, without a pass criterion, because real adopters start cold:

| Profile | Volume | CI | Depth | Passes when |
|---|---|---|---|---|
| Team | 20 changes a day | 30 minutes | `max_depth: 4` | 95th-percentile time to merge under 60 minutes, runs per merged change under 1.5 |
| Slow CI | 5 changes a day | 3 hours | `max_depth: 4` | 95th-percentile time to merge under 4.5 hours, runs per merged change under 1.5 |

A third profile exercises adaptive sizing: 5% broken with `target_pass_rate: 0.8`, on the same eight 30-day seeds, compared seed for seed against a fixed size of 8. PR 1 pins **900 changes a day, 2-minute CI, and `max_depth: 4`** (with the same 5-minute max wait, 1% flaky runs, 50 runners and 1.5× CI timeout), based on the reproducible [capacity report](../research/phase2-capacity.md), so that, with a fixed size of 8, more than half of fresh batches would exceed the adaptive cap while median time to merge stays under four CI durations, as measured in that report. It passes when runs per merged change stay under 1.5 and median time to merge is no worse than with the fixed size.

**Capacity is measured, not asserted.** The simulator publishes strict capacity over defect rates (0.1% to 5%), batch sizes and depths, and any strict throughput target beyond the adopter profiles is pinned from that curve with its evidence. The roadmap's earlier target of 2,000 changes a day at 30-minute CI and 2% broken is superseded rather than carried over. Phase 2b's own exit test, for post-merge verification at agent scale, is the high-volume target.

**Feasibility gates the work.** The simulator drives the real planner, which cannot run chains until PR 3, so PR 1 also ships a standalone capacity model of these rules, documented and checked against the simulator's existing serial results. The model publishes the capacity curve and predicts the adopter profiles, reporting each seed separately and the fraction of changes beyond each 95th-percentile threshold, since the Slow profile's margin sits in that tail. Its evidence is provisional. If it predicts that the adopter profiles fail, this ADR is amended before PR 3 builds the chain. The planner-backed exit test is added in PR 1, skipped until PR 3, and must pass before Phase 2 exits and before any default changes. Parallel off-chain failure isolation (see Alternatives) is evaluated in the model.

**The default changes only on evidence.** `max_depth` becomes 4 by default only when the adopter profiles pass and this project's own queue has run `max_depth: 4` for two weeks and at least 50 changes, with no incorrect landing and with observed runs per change and time to merge no worse than an adjacent period of equal length at depth 1, run immediately before or after the depth-4 period under the same CI configuration. The change ships in a release note, and queues that set `max_depth` keep their setting.

### Visibility

For every change, status shows its batch and position, how many batches are ahead of it, the gate holding it (window full, `max_wait`, waiting behind bisection, approval, or its batch's checks), and a ledger of every CI run it was part of, with each run's outcome and, for wasted runs, why. For every queue, status shows the chain, the current window and batch size, and their inputs: *p̂* and its sample size, and the event that last halved the window.

### Implementation

1. Simulator support for finite runners, cancellation, reruns and the pinned profiles; the standalone capacity model and its report; and the planner-backed exit test, skipped until PR 3.
2. Bounded same-commit check retries.
3. The planner chain with a fixed window, the blame and settlement rules, the new event types and snapshot support, at default depth 1.
4. The cancellation capability, its permission, and the status ledger.
5. The adaptive window and the opt-in adaptive batch size.

### Fixed-window implementation notes (PR 3)

Admission conservatively stops behind any provisional or failed batch, even
with free slots. Only pending bisection halves pass the unresolved-bisection
barrier; unrelated infrastructure retries wait. PR 5 must explicitly decide
whether to retain the conservative admission rule when adding adaptation.

Snapshot support is a codec, not yet connected to store persistence. PR 5 must
add adaptive window state to it. Available runner capacity per repository should
be at least `max_depth` per active base queue, since multiple bases multiply
concurrent runs and forge-side queueing consumes `check_timeout`. The exit evidence
validates depth 4; depths above 4 remain unvalidated. Depth-1 config
keeps its legacy policy digest, and the default remains 1 until the dogfooding
criteria above are met.

### Cancellation and visibility implementation notes (PR 4)

Cancellation is opt-in through speculative admission (`max_depth > 1`). Queues
that start at depth 1, including those with retries, keep their existing bisection
and dequeue protocol and do not cancel CI. Default policy digest, existing wire
fixtures and legacy simulator results remain unchanged.

`batch_runs_observed` records all accepted CI names and attempts, including
non-required workflows, independently of the filtered retry-policy observations.
Empty or unchanged reads emit no history event. History keeps all identities returned
together, while cancellation and policy retain only the latest attempt per producer.
Queues that start at depth 1, including retry-enabled ones, derive their ledger
from existing events and never emit the PR 4 event types. After a downgrade from
a higher depth, in-flight speculative intents keep emitting cancellation events
until they drain. The ledger cannot supply landing evidence.
GitHub enriches every observed Actions job
with its actual attempt on this opt-in path; the default plain reader is unchanged.
The reducer first permanently discards a non-landing speculative batch. Its
recorded latest producer/run attempts become `record_check_cancellation` effects;
`check_cancellation_requested` pins the exact batch, tested SHA, test ref, runs and
reason before `cancel_checks` may call the optional `CheckCanceller`. These effects
survive projection snapshots and journal replay. Retired batch IDs cannot become
landing heads again. A concurrent landing of another batch never broadens the
immutable cancellation targets. `LandingStarted` and landed batches generate no
cancellation intent. Late checks cannot restore a retired batch.

`check_cancellation_reported` distinguishes unsupported capability/permission
from confirmed run completion. POST acceptance remains requested and is retried
idempotently after restart. Pending reads and forge errors complete their action
and persist `check_cancellation_deferred`, which schedules a distinct
`retry_check_cancellation` successor. Its key is unique to the attempt and it is
not planned before its recorded wake time, so even the oldest-ID store claim
order lets newer queue work proceed during backoff. Each mutation or read-only
phase gets eight total attempts, waiting 30s, 1m, 2m, 4m, 8m, 16m and 30m between
them. Exhaustion records `abandoned` with the final reason; it never blocks the
queue. Simulator and production use this same reducer, planner timer and attempt
budget. Confirmation reports may be incremental: the reducer retains completed
subsets and retries only the remaining identities. Overlapping reports from
recovered actions tolerate earlier confirmations, apply newly confirmed targets,
and reject identities outside the original intent. Confirmation identities are
retained only while that mutation intent is pending, including across snapshots.

Unsupported cancellations schedule read-only `observe_retired_checks` until the
recorded attempts finish or that phase exhausts its budget; those historical
`checks_observed` events cannot revive the batch and keep cancellation marked
unsupported while updating its outcome. GitHub validates every target against its Actions job,
workflow, tested SHA, queue ref and current attempt before sending any cancels.
A newer unrecorded attempt, unsupported producer or missing identity is left
alone; this deliberately favors attribution over maximum CI savings. This is
best effort, not a guarantee of stopping CI and never a landing authorization.
The manifest already requests `actions: write` since PR 2 (#43); existing Apps
and installations still need to approve that grant.

Status reconstructs a per-change ledger from committed journal history, including
successor commits containing that change, distinct rerun attempts, outcomes,
cancellation states and wasted-work causes. Legacy aggregate checks are labelled
without fabricated run IDs. API/CLI show the full recorded ledger; the existing
PR check summary includes a bounded eight-row excerpt rather than adding a new
comment channel. Runtime policy supplies the current fixed window, batch size
and holding gate. Approval is evaluated by executor preflight, so the current
`landing-preflight` gate includes pending approval and other final policy checks;
it does not distinguish approval separately. Adaptive inputs remain for PR 5.
Snapshot state holds pending
cancellation effects, not unbounded run history; future retention must define
how historical ledgers are retained.

Journals written by pre-merge heads of PR 4 are intentionally unreadable;
compatibility is claimed only with `origin/main`.

Both planner-backed adopter modes pass. Unsupported totals match PR 3 exactly;
supported cancellation reduces runner-minutes without changing run counts or
latency with 50 runners. See [measured results](../research/phase2-chain.md).

### Adaptive implementation notes (PR 5)

W and the outcome estimator are projections of accepted existing events. A
confirmed checks failure or timeout of an original batch halves W once;
build conflicts leave W unchanged because they say nothing about CI health.
`FromBisection` and a snapshotted per-batch marker prevent repeated decrease.
Every settled landing increments W, including halves. A new queue starts at its
ceiling, idle retains W, and config changes clamp it. Admission counts all open
phases and never discards work merely because W shrank.

`config_applied` gains optional `window: {min_depth, max_depth}` bounds, with its
own additive golden fixture. The digest alone cannot recover historic bounds,
so omitting this information would make config clamping impossible to replay.
Default depth-1 encodings omit it, including retry-enabled depth-1 queues; no new
event/action type is introduced. Before adaptation, old speculative journals keep
their fixed admission policy. Reconciliation records a one-time same-digest bounds
initialization at the old ceiling without invalidating admitted work or resetting
outcomes. When the first reconciliation also changes policy, it reads and verifies
the historical policy at the recorded config commit, initializes its fixed ceiling
under the old digest, then applies the replacement bounds. A serial queue enabling
speculation clamps W=1 rather than restarting
at the higher ceiling. Subsequent policy transitions use the recorded bounds.

Fresh size uses the last 200 landed or checks/timeout-dequeued changes in log
order; conflict dequeues are excluded. Fewer than 20 samples use `max_size`, and a
digest change restarts the ring. Pending groups are not resized by the estimator;
policy changes retain the existing invalidation and regrouping protocol. The target's
shortest canonical decimal (`strconv.FormatFloat`, IEEE-754 round-trip) is read as
an exact rational; integer powers compare `(samples − failures)^k / samples^k`
with that rational. Equality passes. No floating-point pow/log or tolerance can
flip a replay decision. Tests cover exact equality and the adjacent representable
targets on each side.

**Admission decision: retain the conservative barrier.** In an eight-seed one-day
planner experiment with the pinned rates, relaxing the provisional/failed-batch
barrier increased adaptive runs 4,981 → 5,016 and wasted runs 2,508 → 2,543,
with slightly worse median and p95; Team and Slow were unchanged. This is a
non-gating policy experiment, separate from the full 30-day exit profiles.
Building fresh work on a known failing prefix offers no measured benefit here.
The overlay, exact results and limitations are recorded in
[phase2-chain.md](../research/phase2-chain.md).

**Snapshot decision: keep store persistence out of scope.** The version-2 codec
includes W, recorded bounds/upgrade state, the last window failure, per-batch
once-only markers and the last-200 outcome ring. It validates bounds, event
revisions, outcome identity/order and duplicates. Every snapshot test compares
full encoded bytes after decode/encode, including a state holding retained
cancellation confirmations. Connecting store compaction would also require a
ledger-retention decision; it is not a safe incidental change.

API, CLI and the bounded existing PR check summary show W, fresh size, p̂ with
its failures/sample count and target, the last halving event's queue-fold revision,
batch and reason, and the chain. No summary comment channel is added.

The planner-backed adaptive exit runs with `-sim-adaptive-full-exit`: seeds 1–8,
30 days each, in both cancellation modes and paired against fixed size 8, with
per-seed and pooled median/cost assertions and an additional p95-vs-fixed
regression assertion. The first-day adaptive harness runs in `make check`; the
full paired gate is opt-in because it is substantially more expensive. All
workload pins and ADR pass thresholds are unchanged. Large simulations use an
opt-in fake-forge tree/PR-marker delta codec and delta-derived synthetic commit
IDs to bound memory and hashing, with full changed-file and ancestry invariants;
legacy fake identities are unchanged. Immutable observation shards avoid copying
all historical PR observations on every event while preserving re-admission and
snapshot bytes. The measured equilibrium, cold starts, compatibility checks and
adopter deltas are in the results report. The default `max_depth` remains 1.

### Dogfooding notes

On 2026-10-17 the comparison period for the default change was relaxed from the
period *preceding* the depth-4 run to an *adjacent* one, before or after it. This
project's queue had only about a day and a half of depth-1 history, and #46 changed
its required CI (`check` plus `sim-exits`), so no comparable preceding period
existed. The queue therefore runs `max_depth: 4` first and a depth-1 period of
equal length afterwards, both under the same CI configuration. The plan, metrics
and log are in [the dogfooding report](../research/phase2-dogfood.md).

## Consequences

- At the adopters' volumes, a change queued behind others waits roughly one CI run plus queueing, not one CI run per batch ahead of it.
- Strict capacity is bounded by failure resolution, not depth, so high volume at realistic defect rates needs post-merge verification. This ADR says so and supersedes the earlier 2,000-a-day strict target.
- CI cost rises with the window and falls when failures shrink it. `max_depth` is the ceiling, and the default does not move without the evidence above.
- Landing safety does not change: one batch lands at a time, through the same fence, onto exactly the commit it was built on.
- The planner keeps more state: the chain, per-batch checks state, and the outcome history for the window and size. All of it derives from the log, so replay stays deterministic.
- New event and action types arrive for chains. Single-batch queues never write them.
- The App gains `actions: write` on GitHub for cancellation.

## Alternatives considered

- **More parallel independent batches** (each built on the base, as with Mergify's parallel checks). Each batch must land on a base that has moved since it was built, so its result is stale by the time it lands. That breaks strict verification or forces retesting.
- **A fixed speculation depth.** Simpler, but it either wastes CI during failure bursts or leaves throughput unused in quiet periods.
- **Optimistic failure resolution** (Zuul's HEADGREEN: a failed batch is carried by a passing successor that contains it). Once failures are confirmed by a rerun, a passing successor over a twice-failed batch is rare and does not show the failure was a flake. It would also need its own landing contract, leased on the failed batch's base and covering both batches' members. It is deferred until check-retry data shows a need.
- **Predictive scheduling** (Uber's SubmitQueue). It can beat a window but needs models trained on history the queue does not have yet. It stays in Phase 6, and the outcome history recorded here is its input.
- **Strict speculation as the answer to agent-scale volume.** It cannot reach that volume at agent-scale defect rates; post-merge verification can.
- **Parallel off-chain failure isolation.** When the oldest batch fails, take its changes out of the chain at once, rebuild the batches behind it without them, and test each change alone in parallel on the current base, returning passers to the front of the queue. This removes bisection from the chain's critical path. It costs more runs per failure and adds a new state for changes in isolation. The standalone capacity model evaluates it in PR 1, and it is adopted by amending this ADR if the capacity curve shows a material gain.
