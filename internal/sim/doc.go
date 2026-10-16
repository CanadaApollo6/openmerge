// Package sim is a deterministic, discrete-event simulator for the merge queue.
//
// It wires the real planner (internal/queue), the real executor
// (internal/executor), and the in-memory fake forge together on a virtual
// clock, with every random choice drawn from a seeded source, so a seed always
// reproduces the same run.
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
//
// After every step the simulator checks its invariants: main contains only
// commits covered by a tested tip whose CI passed and no broken PR; no PR lands
// twice; a PR lands only with the head that was tested; no untrusted PR enters a
// test ref; and the queue never stalls with work queued and no pending timer.
// Landed events must
// match the actual base transition and fence, and each forge landing must have
// a Landed event after recovery settles. Abandoned attempts must never land.
//
// Result JSON includes the full effective config, seed, and durations in seconds.
// Dequeued counts acknowledged dequeues, including re-pushes later merged.
//
// CI currently has unlimited runners; runner capacity is left for Phase 2.
package sim
