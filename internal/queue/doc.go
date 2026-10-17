// Package queue implements a merge queue as a pure event fold and planner. The default window has one active batch.
// Apply copies the state it changes; Plan reads state and an explicit clock and
// returns deterministic, keyed actions. Build actions carry the configured merge
// method; build results pin the actual method for landing. Neither function
// performs I/O. See docs/adr/0002-storage-and-state-model.md and
// docs/adr/0004-forge-driver-interface.md for the persistence and fencing contracts.
//
// With SpeculationMaxDepth above one, Active is the oldest unlanded batch and
// Following is its ordered suffix. The adaptive window counts every admitted batch,
// including passed and landing batches. Each successor is built on its parent's
// LandingSHA, distinct from TestedSHA for fast-forward markers. Only Active can
// land, on exactly its recorded BaseSHA after its predecessor settled through
// the fence. Own in-flight base observations do not settle attempts. An outside
// move marks all cancellable batches invalid, preserving any durable landing.
//
// ChainedBatchStarted admits work before build I/O; ChainedTestRefBuilt records
// the resulting tested and landing commits and repeats the parent and position.
// BuildChainedTestRef uses the same git build and landing path as depth one.
// InvalidateBatches/BatchesInvalidated record exact discarded IDs and cause;
// bounded recent invalidations support status while the log retains history.
// The invalidation marker blocks eligibility immediately, before its action is
// executed. Late results cannot revive it; a late build result reissues cleanup.
// Retired work requests best-effort cancellation of its identified runs.
// Admission stops behind any provisional or failed batch. Unrelated build retries
// wait for all bisection halves to resolve.
//
// Only the settled oldest batch consumes a same-commit retry or diagnoses a
// failure. Later check results and conflicts are provisional. A confirmed oldest
// failure invalidates the suffix, then splits or dequeues as in serial mode.
// DiagnoseBatch/BatchDiagnosed promote provisional build failures once
// they reach the head. Pending halves form a new chain ahead of fresh work; no
// fresh batches are admitted while a half remains unresolved. Leaving changes
// invalidate their batch and suffix; config changes invalidate all non-landing
// batches and clear old groups. invalidateFrom is the future approval-gate hook.
// EncodeSnapshot/DecodeSnapshot preserve the compact chain, per-batch evidence,
// retry state, pending groups, fences, effects and bounded tested-SHA history.
// The remaining description also defines the unchanged depth-one protocol.
//
// Plan chooses the first BatchMaxSize eligible, trusted pinned heads in queue
// order (default size 1). Formation skips entries that are currently ineligible,
// untrusted, or awaiting dequeue, relaxing strict FIFO compared with serial mode.
// Partial batches wait until the oldest runnable entry has waited
// BatchMaxWait; NextWake exposes that deadline. Size 1 never waits. A build key
// hashes the base and ordered composition, so revalidation cancels stale proposals.
// The executor durably records BatchStarted before building. Only that event
// creates a Building batch and advances the monotonic batch ID; Apply never
// recomputes composition from configuration. TestRefBuilt enters Testing; a matching successful
// check enables landing. TestRefBuilt.At and LandingStarted.At carry the recorded
// timer origins, so replay never substitutes the current clock.
//
// Checks observed before their build result are retained by tested SHA. At most
// 64 distinct SHAs are retained per active batch; the least recently observed SHA
// is evicted first. Matching outcomes are consumed by TestRefBuilt. Discarding or
// completing the batch drops all remaining outcomes; idle queues retain none.
// Retention only improves latency: a Testing batch without an outcome requests
// RefreshChecks immediately and then every RefreshInterval (default 1m). The
// executor records ChecksCompleted or ChecksReopened first, then ChecksRefreshed
// stamped at read time for every successful read. Observations fold strictly in
// event-log order, including retained pre-build results; timestamps never suppress
// them. Every reopen revokes success and invalidates timeout evidence without
// extending the original deadline. A stale pending costs another read; a delayed
// pass can restore projected success. Each matching reopen increments the batch
// checks generation, pinned in the Land key and payload. Both store start gates
// re-plan under the queue lock, so an old Land stays stale after a delayed pass;
// a new Land must freshly re-read checks before LandingStarted. A delayed older
// terminal failure after a passing rerun still fails the batch conservatively.
// Refresh keys use the batch and count of recorded refreshes; every matching
// ChecksRefreshed advances the count, even if its read time went backward. Timeout
// requires that refresh's own At to be at or after the deadline, with no reopen
// applied since it. Pending Land preflights also record Reopened then Refreshed
// at read time, so repeated passing-refresh/pending-preflight flips time out. A
// passing refresh after the deadline may land if its preflight also passes.
// Until qualifying timeout evidence, Plan requests a refresh even if the normal
// interval has not elapsed. Worker clocks affect timeout liveness, as StartedTestingAt
// already does, never landing safety. Equal timestamps have no special meaning.
//
// Retry-enabled queues use identified ChecksObserved instead of ChecksCompleted.
// Failed producer/run/attempt identities are retained separately from the latest
// observation. RecordCheckRerun and its queue-locked CheckRerunRequested consume
// one retry and pin its own deadline before RerunChecks dispatches it. Per-target
// CheckRerunDispatchProgress fences each Actions POST with a durable intent and
// records acceptance; ambiguous outcomes require provider reconciliation.
// A delayed
// observation of an original run can neither confirm failure nor replace a rerun
// pass. Each originally failed producer must supply a newer attempt on the same
// tested SHA and test ref; Land preflight checks those identities too. Reopening
// advances the landing generation but preserves the retry request, failed runs,
// original deadline and rerun deadline. ConfirmCheckFailure durably diagnoses a
// failed rerun, an unsupported capability or a qualifying timeout; confirmation
// cannot be revoked. Passing batches waiting to land never time out. The
// retryOldestBatch policy hook defers provisional failures until that batch
// is oldest. QueueConfig supplies the effective retry limit to planner and
// executor, allowing the chain to enable it in one policy assignment. With retries disabled,
// existing events, actions, keys and replay behavior are unchanged.
//
// Non-conflict build failures keep PRs queued. Plan backs off from the recorded
// TestRefBuildFailed.At by BuildBackoff, doubling per consecutive failure (with
// duration saturation). BuildRetries limits failures, defaulting to 3. At the limit,
// State.PauseReason returns "build-failed" and Plan reports the pause on the head PR
// without building. The pause is derived from Config and the recorded streak,
// because Apply has no config input. An explicit refresh clears the streak and
// delay through a current-basis BlockSourceCleared for build-failed. Legacy
// ResumeRequested, a changed BaseSHA or effective config also reset retries;
// duplicate BaseMoved observations do nothing. Successful
// builds also reset it. Checks failures, timeouts and build conflicts split a
// multi-PR batch into two ordered pending halves, without dequeuing its members.
// Pending groups run before fresh work, first half then second, with no batch wait.
// A passing sub-batch lands its tested tip once; a failing singleton is dequeued
// with its precise reason. FailBatch/BatchFailed make timer-driven splits durable.
// Batch.FromBisection records provenance for metrics. Invalidated members leave
// their pending groups. Base moves, abandoned landings and infrastructure retries
// rebuild only the current composition, preserving its bisection provenance.
//
// Legacy ActionPoisoned records repeated invalid durable work. It pauses all work except
// the head PR's paused ReportStatus and resolution of an in-flight landing.
// Its reason is "action-poisoned"; further poison events are ignored until
// ResumeRequested clears this pause. The active batch survives.
// PauseRequested shares that path with an explicit reason and operator detail.
// New observations use BlockSourceBlocked and BlockSourceCleared, independently
// revisioned like rules sources. Config and check-source blocks clear on observed
// recovery; operator, poison and build retries clear on explicit refresh. Landing blocks require
// fresh ancestry verification of their durable subject before refresh clears them.
// Legacy shared pauses project as the operator source without changing replay.
// RefreshRequested revokes cached check success and requests an early refresh.
// Pausing stops new work; it never stops settling a landing already in flight.
// Every Action validates its semantic inputs. Plan checks this invariant before
// emitting actions; invalid state/config inputs panic rather than emit unsafe work.
// Wire v1 requires every field, including zero/empty values, and rejects JSON null.
// DecodeAction also validates the restored action before it can be executed.
//
// LandingStarted is the durable transition into Landing, before any forge call.
// Cancellation and invalidation wait for its definitive result. After ResolveAfter,
// Plan requests fence-and-read resolution; a deadline alone never fails a landing.
// A rejected push follows the same resolution. Only fenced abandonment rebuilds
// a fresh batch or allows a pending dequeue; a lease refusal alone settles nothing.
// The executor generates a random attempt ID before recording LandingStarted.
// A batch in Landing can only resolve that recorded ID, never start another Land.
//
// Every Dequeue is completed with Dequeued, which removes the entry and rebuilds
// the remaining members of its batch. Until that result, the entry stays queued
// and cannot join another batch.
// ReportStatus, MarkLanded, and DeleteRef repeat until ActionCompleted acknowledges
// their keys. Squash/rebase Landed events record a MarkLanded effect per pinned PR
// so PR closing survives a crash. Merge landings need none. Status and dequeue
// actions also pin the head SHA, with normalized lifecycle states and stable reasons.
// Acknowledgement removes the pending effect; retained keys are pruned when their
// batch or queued PR disappears, keeping the acknowledgement set bounded. Executors
// must honor the event watermark in ADR 0002 before executing consequential actions.
//
// # Invariants
//
// Apply owns admissions, recorded batch composition, timers, and phase transitions;
// Plan owns time/config-dependent decisions and never changes state. Admissions
// and batches have separate monotonic IDs. Build and landing results must match
// the current BatchID and phase; each batch allows only one landing attempt.
// ResolveLanding carries that attempt ID, fence SHA, and tested SHA. Dequeued must
// echo the entry's admission ID, plus the BatchID for timeout actions, so late
// results cannot remove a new subject. ActionCompleted echoes the exact effect
// key. Build keys include the method and composition, dequeue keys include the admission, and
// report keys distinguish both admissions and outcome revisions. Landing always
// carries the tested batch's base, tested SHA, and method, together with the
// current fence and checks generation (required checks_generation in wire v1).
//
// Test commits are unique per batch: the executor includes the batch ID in the
// top commit of every test ref. As a guard against an executor that breaks this,
// Apply remembers the last 64 tested SHAs and rejects reuse of one as a
// non-conflict build failure with an explanatory report.
// Late outcomes for discarded or completed batches are not retained. Snapshots
// must preserve pending groups and the tested-SHA history as well as batch and admission counters.
package queue

// W starts at max_depth, grows per landing, and halves once per original-batch
// confirmed failure. Logged config bounds clamp it at a policy transition. The
// last 200 checks/timeout dequeues and landed changes determine opt-in batch size
// through exact rational comparisons. Snapshots retain both bounded projections.
