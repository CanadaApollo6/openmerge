// Package queue implements the serial merge queue as a pure event fold and planner.
// Apply copies the state it changes; Plan reads state and an explicit clock and
// returns deterministic, keyed actions. Build actions carry the configured merge
// method; build results pin the actual method for landing. Neither function
// performs I/O. See docs/adr/0002-storage-and-state-model.md and
// docs/adr/0004-forge-driver-interface.md for the persistence and fencing contracts.
//
// Apply allocates a Building batch as soon as the first runnable entry exists,
// using a monotonically increasing ID. This makes build keys stable without
// allowing Plan to mutate state. TestRefBuilt enters Testing; a matching successful
// check enables landing. TestRefBuilt.At and LandingStarted.At carry the recorded
// timer origins, so replay never substitutes the current clock.
//
// Checks observed before their build result are retained by tested SHA. At most
// 64 distinct SHAs are retained per active batch; the least recently observed SHA
// is evicted first. Matching outcomes are consumed by TestRefBuilt. Discarding or
// completing the batch drops all remaining outcomes; idle queues retain none.
//
// Non-conflict build failures keep PRs queued. Plan backs off from the recorded
// TestRefBuildFailed.At by BuildBackoff, doubling per consecutive failure (with
// duration saturation). BuildRetries limits failures, defaulting to 3. At the limit,
// State.PauseReason returns "build-failed" and Plan reports the pause on the head PR
// without building. The pause is derived from Config and the recorded streak,
// because Apply has no config input. ResumeRequested and BaseMoved clear the streak
// and delay; successful builds also reset it. Conflicts still dequeue normally.
//
// LandingStarted is the durable transition into Landing, before any forge call.
// Cancellation and invalidation wait for its definitive result. After ResolveAfter,
// Plan requests fence-and-read resolution; a deadline alone never fails a landing.
// Rejection or fenced abandonment rebuilds a fresh batch unless a dequeue is pending.
//
// Every Dequeue is completed with Dequeued, which removes the entry and clears its
// batch. Until that result, the entry stays queued and cannot start another batch.
// ReportStatus and DeleteRef repeat until ActionCompleted acknowledges their keys.
// Acknowledgement removes the pending effect; retained keys are pruned when their
// batch or queued PR disappears, keeping the acknowledgement set bounded. Executors
// must honor the event watermark in ADR 0002 before executing consequential actions.
//
// # Invariants
//
// Apply owns admissions, batch allocation, recorded timers, and phase transitions;
// Plan owns time/config-dependent decisions and never changes state. Admissions
// and batches have separate monotonic IDs. Build and landing results must match
// the current BatchID and phase; each batch allows only one landing attempt.
// ResolveLanding carries that attempt ID. Dequeued must echo the entry's admission
// ID, plus the BatchID for timeout actions, so late results cannot remove a new
// subject. ActionCompleted echoes the exact effect key. Build keys include the
// method, dequeue keys include the admission, and report keys distinguish both
// admissions and outcome revisions. Landing always carries the tested batch's
// base, tested SHA, and method, together with the current fence.
package queue
