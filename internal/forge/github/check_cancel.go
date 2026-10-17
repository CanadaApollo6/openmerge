package github

import (
	"context"
	"errors"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

var _ forge.CheckCanceller = (*Driver)(nil)

// CancelChecks only targets Actions workflows attributed to the recorded
// checks. A repeated POST is harmless; a 202 response never confirms stopping.
func (d *Driver) CancelChecks(ctx context.Context, repo forge.RepoRef, req forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.CancelChecksResult{}, err
	}
	if req.TestedSHA == "" || req.TestRef == "" || req.Key == "" || len(req.Runs) == 0 {
		return forge.CancelChecksResult{}, errors.New("tested SHA, test ref, key, and recorded runs are required")
	}
	d.writes.Lock()
	defer d.writes.Unlock()
	unsupported := func(detail string) (forge.CancelChecksResult, error) {
		return forge.CancelChecksResult{Unsupported: detail}, nil
	}
	for _, requested := range req.Runs {
		if requested.Source != "app:15368" || requested.ID == 0 || requested.WorkflowRunID == 0 {
			return unsupported("cancellation requires an identified GitHub Actions workflow run")
		}
		if requested.HeadBranch != req.TestRef {
			return unsupported("check is not bound to the requested test ref")
		}
	}
	checks, err := d.CheckRuns(ctx, repo, req.TestedSHA)
	if err != nil {
		return forge.CancelChecksResult{}, err
	}
	ids := make(map[uint64]bool)
	for _, requested := range req.Runs {
		if !slices.ContainsFunc(checks, func(current forge.CheckRun) bool {
			return current.ID == requested.ID && current.Name == requested.Name && current.Source == requested.Source && current.HeadBranch == req.TestRef && current.WorkflowRunID == requested.WorkflowRunID
		}) {
			return unsupported("exact recorded Actions check is no longer observable")
		}
		ids[requested.WorkflowRunID] = true
	}
	ordered := make([]uint64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	workflows := make(map[uint64]*gh.WorkflowRun)
	// Validate every target before dispatching any mutation.
	for _, id := range ordered {
		run, _, err := d.client.Actions.GetWorkflowRunByID(ctx, owner, name, int64(id))
		if err != nil {
			if isUnsupportedRerunError(err) {
				return unsupported("Actions permission not granted; CI cancellation unavailable")
			}
			return forge.CancelChecksResult{}, safeError(err)
		}
		if run.GetID() != int64(id) || run.GetHeadSHA() != req.TestedSHA || run.GetHeadBranch() != req.TestRef || run.GetRunAttempt() < 1 {
			return forge.CancelChecksResult{}, errors.New("github Actions workflow does not match the recorded commit and queue ref")
		}
		for _, requested := range req.Runs {
			if requested.WorkflowRunID == id && effectiveGitHubAttempt(requested.Attempt) != uint64(run.GetRunAttempt()) {
				return unsupported("recorded Actions attempt is no longer current; refusing to cancel another attempt")
			}
		}
		workflows[id] = run
	}
	var result forge.CancelChecksResult
	for _, id := range ordered {
		run := workflows[id]
		if run.GetStatus() == "completed" {
			for _, requested := range req.Runs {
				if requested.WorkflowRunID == id {
					index := slices.IndexFunc(checks, func(current forge.CheckRun) bool {
						return current.ID == requested.ID && current.Name == requested.Name && current.Source == requested.Source && current.HeadBranch == req.TestRef && current.WorkflowRunID == id
					})
					// A workflow conclusion is aggregate: successful jobs can
					// belong to a failed/cancelled workflow. A retained pending
					// job observation must wait for its own terminal evidence.
					if checks[index].State == forge.Pending {
						continue
					}
					requested.State = checks[index].State
					result.Confirmed = append(result.Confirmed, requested)
				}
			}
			continue
		}
		if _, err := d.client.Actions.CancelWorkflowRunByID(ctx, owner, name, int64(id)); err != nil {
			var accepted *gh.AcceptedError
			if errors.As(err, &accepted) {
				continue
			}
			if isUnsupportedRerunError(err) {
				var response *gh.ErrorResponse
				errors.As(err, &response)
				result.Unsupported = "GitHub Actions cancellation refused: " + d.app.sanitizer.Sanitize(response.Message)
				return result, nil
			}
			// Completion may race with POST. Confirm only with a fresh read.
			var response *gh.ErrorResponse
			if errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == 409 {
				return result, nil
			}
			return result, safeError(err)
		}
	}
	return result, nil
}
