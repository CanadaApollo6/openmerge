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
// Retention only improves latency: a Testing batch without an outcome requests
// RefreshChecks immediately and then every RefreshInterval (default 1m). The
// executor records any terminal ChecksCompleted first, then records ChecksRefreshed
// for every successful read, including pending checks. Refresh keys use the batch
// and count of recorded refreshes. A checks timeout requires a refresh at or after
// its deadline; until then Plan requests a refresh even if the normal interval
// has not elapsed.
//
// Non-conflict build failures keep PRs queued. Plan backs off from the recorded
// TestRefBuildFailed.At by BuildBackoff, doubling per consecutive failure (with
// duration saturation). BuildRetries limits failures, defaulting to 3. At the limit,
// State.PauseReason returns "build-failed" and Plan reports the pause on the head PR
// without building. The pause is derived from Config and the recorded streak,
// because Apply has no config input. ResumeRequested and a changed BaseSHA clear
// the streak and delay; duplicate BaseMoved observations do nothing. Successful
// builds also reset it. Conflicts still dequeue normally.
//
// ActionPoisoned records repeated invalid durable work. It pauses all work except
// the head PR's paused ReportStatus and resolution of an in-flight landing.
// Its reason is "action-poisoned"; further poison events are ignored until
// ResumeRequested clears this pause. The active batch survives.
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
// Every Dequeue is completed with Dequeued, which removes the entry and clears its
// batch. Until that result, the entry stays queued and cannot start another batch.
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
// Apply owns admissions, batch allocation, recorded timers, and phase transitions;
// Plan owns time/config-dependent decisions and never changes state. Admissions
// and batches have separate monotonic IDs. Build and landing results must match
// the current BatchID and phase; each batch allows only one landing attempt.
// ResolveLanding carries that attempt ID, fence SHA, and tested SHA. Dequeued must
// echo the entry's admission ID, plus the BatchID for timeout actions, so late
// results cannot remove a new subject. ActionCompleted echoes the exact effect
// key. Build keys include the method, dequeue keys include the admission, and
// report keys distinguish both admissions and outcome revisions. Landing always
// carries the tested batch's base, tested SHA, and method, together with the
// current fence.
//
// Test commits are unique per batch: the executor includes the batch ID in the
// top commit of every test ref. As a guard against an executor that breaks this,
// Apply remembers the last 64 tested SHAs and rejects reuse of one as a
// non-conflict build failure with an explanatory report.
// Late outcomes for discarded or completed batches are not retained. Snapshots
// must preserve the tested-SHA history as well as the batch and admission counters.
package queue
