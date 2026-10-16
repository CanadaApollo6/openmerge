// Package sim is a deterministic, discrete-event simulator for the merge queue.
//
// It wires the real planner (internal/queue), the real executor
// (internal/executor), and the in-memory fake forge together on a virtual
// clock, with every random choice drawn from a seeded source, so a seed always
// reproduces the same run.
// Start-event emits revalidate the action's key and complete payload against
// the current plan before any build or landing I/O. Cancelled starts append
// nothing. Published action keys must retain their original payload, and stale
// batch failures must not change a newer batch.
//
// The default workload is 500 trusted PRs arriving over 24 hours, 5% of them
// broken, no flakes, and two-minute CI (720 serial runs per day). One untrusted
// fork PR is added per 50 PRs; those must be blocked before testing. A broken
// PR fails CI for any test commit that contains it; a flake fails a good commit
// with a configured probability. Failed CI, including flakes, dequeues with
// checks-failed: automatic retries belong to a later phase.
//
// Faults inject per-method forge errors, lost landing acknowledgements, delayed
// pushes, and duplicate executions. Recovery must fence off abandoned attempts;
// their captured requests are replayed later and must be rejected. DropWebhookRate
// skips CI completion notifications so only RefreshChecks can find the outcome.
// RepushRate updates queued PR heads and re-enqueues them after a head-changed
// dequeue. ConflictRate selects adjacent PR pairs that edit a shared file,
// producing a conflict for the second landing.
// ExternalPushRate selects a fraction of trusted PRs accompanied by an external
// fast-forward of main half a CI interval after their scheduled enqueue, and another
// immediately after their first LandingStarted but before its push. Each push
// adds a distinct file and delivers BaseMoved so the queue rebuilds on the new
// base. External pushes retain existing commits and are not queue landings.
//
// After every forge write and external push, main must retain every previously
// observed main tip as an ancestor. After every step, queue landings contain only
// commits covered by a tested tip whose CI passed and no broken PR; no PR lands
// twice; a PR lands only with the head that was tested; no untrusted PR enters a
// test ref; and the queue never stalls with work queued and no pending timer.
// Landed events must
// match the actual base transition and fence, and each forge landing must have
// a Landed event after recovery settles. Abandoned attempts must never land.
// Exit tests run all 500-PR variants with both size 1 and size 8 (30s max wait),
// and bound runs per merge and median latency for a 2,000-PR day with size 8
// and 30-minute CI. BisectionCIRuns counts
// builds from pending bisection groups, including rebuilds of those groups.
// Failure dequeues must isolate a singleton; settled workloads must account for
// every admitted PR and retain merged reports for every landed PR.
//
// CI membership must match the planner's intended batch. Before crediting a
// landing, every intended PR must have its file changes in the landed tip; merge
// landings must also retain each intended PR head as an ancestor.
//
// Result JSON includes the full effective config, seed, and durations in seconds.
// Dequeued counts acknowledged dequeues, including re-pushes later merged.
// MeanTimeToMerge, MedianTimeToMerge, and P95TimeToMerge measure the latest
// enqueue through landing, resetting after a head-changed dequeue/re-enqueue.
// MedianTimeFromFirstEnqueue and P95TimeFromFirstEnqueue include the entire wait
// from first enqueue through landing, including earlier admissions. Their JSON
// keys are median_time_from_first_enqueue_seconds and
// p95_time_from_first_enqueue_seconds. Both metric families include only merged
// PRs, use the midpoint median, and use nearest-rank p95.
//
// CI currently has unlimited runners; runner capacity is left for Phase 2.
//
// scripts/check-simulator-mutations.py replays reviewer mutations of the planner,
// executor, and fake forge through Go overlays and confirms the simulator rejects
// each one. Run it after changing an invariant or a fault.
package sim
