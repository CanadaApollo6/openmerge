package fake

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.CheckCanceller = (*Forge)(nil)

// CancelChecks synchronously completes identified pending runs. Matching the
// entire request before changing any run prevents a partial malformed request
// from cancelling unrelated work; repeated requests simply observe completion.
func (f *Forge) CancelChecks(ctx context.Context, repo forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CancelChecks"); err != nil {
		return forge.CancelChecksResult{}, err
	}
	if req.TestedSHA == "" || req.TestRef == "" || req.Key == "" || len(req.Runs) == 0 {
		return forge.CancelChecksResult{}, errors.New("tested SHA, test ref, key, and recorded runs are required")
	}
	r := f.repo(repo)
	indices := make([]int, 0, len(req.Runs))
	for _, requested := range req.Runs {
		if requested.ID == 0 || requested.Name == "" || requested.Source == "" || strings.HasPrefix(requested.Source, "status:") || requested.HeadBranch != req.TestRef {
			return forge.CancelChecksResult{Unsupported: "cancellation requires a recorded check bound to the test ref"}, nil
		}
		i := slices.IndexFunc(r.checks[req.TestedSHA], func(current forge.CheckRun) bool {
			return current.ID == requested.ID && effectiveAttempt(current.Attempt) == effectiveAttempt(requested.Attempt) && current.Name == requested.Name && current.Source == requested.Source && current.HeadBranch == req.TestRef && current.WorkflowRunID == requested.WorkflowRunID
		})
		if i < 0 {
			return forge.CancelChecksResult{Unsupported: "exact recorded check is no longer observable"}, nil
		}
		switch r.checks[req.TestedSHA][i].State {
		case forge.Pending, forge.Success, forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral:
		default:
			return forge.CancelChecksResult{Unsupported: "recorded check state is unknown"}, nil
		}
		indices = append(indices, i)
	}
	var result forge.CancelChecksResult
	for _, i := range indices {
		run := &r.checks[req.TestedSHA][i]
		if run.State == forge.Pending {
			run.State = forge.Cancelled
		}
		result.Confirmed = append(result.Confirmed, *run)
	}
	return result, nil
}
