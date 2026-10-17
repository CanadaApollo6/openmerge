package forge

import "context"

// CancelChecksRequest pins recorded runs of one invalidated batch. Callers must
// durably record intent and exclude landing/landed batches before invoking the
// capability. Drivers must never broaden it to another commit, ref, or attempt.
type CancelChecksRequest struct {
	TestedSHA, TestRef string
	Runs               []CheckRun
	Key                string
}

// CancelChecksResult separates a provider limitation from positive evidence
// that a recorded run finished. Accepting a cancellation request is not that
// evidence: only a forge observation of cancellation or completion confirms it.
// Confirmed runs retain their recorded identity and their observed outcome.
type CancelChecksResult struct {
	Unsupported string
	Confirmed   []CheckRun
}

// CheckCanceller is an optional, best-effort, idempotent capability. It never
// authorizes landing, and success alone does not prove that a run stopped.
type CheckCanceller interface {
	CancelChecks(context.Context, RepoRef, CancelChecksRequest) (CancelChecksResult, error)
}
