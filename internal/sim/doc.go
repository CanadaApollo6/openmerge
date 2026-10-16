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
// Faults inject transient forge errors and crashes after a landing push but
// before its result is recorded, which the executor must resolve through the
// fence. DropWebhookRate skips CI completion notifications so only RefreshChecks
// reconciliation can find the outcome.
//
// After every step the simulator checks its invariants: main contains only
// tested commits whose CI passed and no broken PR; no PR lands twice; a PR lands
// only with the head that was tested; no untrusted PR enters a test ref; and the
// queue never stalls with work queued and no pending timer.
package sim
