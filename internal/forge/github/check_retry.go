package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// RerunFailedChecks uses a durable intent per Actions workflow, committed
// before POST. An uncertain intent can only be reconciled by a fresh read; an
// unchanged failed run never authorizes a second POST, including after restart,
// unless a definite non-dispatch was durably recorded.
func (d *Driver) RerunFailedChecks(ctx context.Context, repo forge.RepoRef, req forge.RerunChecksRequest) (forge.RerunChecksResult, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.RerunChecksResult{}, err
	}
	if req.TestedSHA == "" || req.TestRef == "" || req.Key == "" || len(req.Runs) == 0 {
		return forge.RerunChecksResult{}, errors.New("tested SHA, test ref, key, and failed runs are required")
	}
	d.writes.Lock()
	defer d.writes.Unlock()
	unsupported := func(detail string) (forge.RerunChecksResult, error) {
		return forge.RerunChecksResult{Unsupported: true, Detail: detail}, nil
	}
	for _, requested := range req.Runs {
		if requested.RetryUnsupported != "" {
			return unsupported(requested.RetryUnsupported)
		}
		if strings.HasPrefix(requested.Source, "status:") {
			return unsupported("commit statuses cannot be rerun; retries support GitHub Actions checks only")
		}
		if requested.Source != "app:15368" {
			return unsupported("non-Actions check runs cannot be rerun; retries support GitHub Actions checks only")
		}
		if requested.ID == 0 || !rerunnableCheckState(requested.State) {
			return unsupported("failed Actions check identity is unavailable")
		}
		if requested.HeadBranch != req.TestRef {
			return unsupported("check is not bound to the requested test ref")
		}
	}
	checks, err := d.CheckRuns(ctx, repo, req.TestedSHA)
	if err != nil {
		return forge.RerunChecksResult{}, err
	}
	targets := make(map[uint64]forge.RerunTarget)
	for _, target := range req.Targets {
		targets[target.WorkflowRunID] = target
	}
	ids := make(map[uint64]bool)
	originalAttempts := make(map[uint64]uint64)
	for _, requested := range req.Runs {
		workflowID := requested.WorkflowRunID
		var exact *forge.CheckRun
		newer := false
		for i := range checks {
			check := &checks[i]
			if check.Name != requested.Name || check.Source != requested.Source || check.HeadBranch != req.TestRef {
				continue
			}
			if check.ID == requested.ID {
				exact = check
				if workflowID == 0 {
					workflowID = check.WorkflowRunID
				}
			}
			if newerGitHubCheck(*check, requested) {
				newer = true
			}
		}
		if workflowID == 0 {
			return unsupported("validated GitHub Actions workflow run identity is unavailable")
		}
		if _, exists := targets[workflowID]; !exists && (newer || exact != nil && !rerunnableCheckState(exact.State)) {
			continue
		}
		if exact != nil && exact.WorkflowRunID != workflowID {
			return forge.RerunChecksResult{}, errors.New("workflow target differs from the recorded check")
		}
		if exact == nil && len(req.Targets) == 0 {
			return forge.RerunChecksResult{}, errors.New("exact failed Actions check is not currently observable")
		}
		ids[workflowID] = true
		originalAttempts[workflowID] = max(originalAttempts[workflowID], effectiveGitHubAttempt(requested.Attempt))
	}
	ordered := make([]uint64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	var uncertain error
	for _, id := range ordered {
		target, exists := targets[id]
		if exists && target.Accepted {
			continue
		}
		run, _, err := d.client.Actions.GetWorkflowRunByID(ctx, owner, name, int64(id))
		if err != nil {
			if isUnsupportedRerunError(err) {
				return unsupported("Actions permission not granted; failed-check reruns unavailable")
			}
			return forge.RerunChecksResult{}, safeError(err)
		}
		if run.GetHeadSHA() != req.TestedSHA || run.GetHeadBranch() != req.TestRef || run.GetRunAttempt() < 1 {
			return forge.RerunChecksResult{}, errors.New("github Actions workflow does not match the recorded commit and queue ref")
		}
		if uint64(run.GetRunAttempt()) < originalAttempts[id] {
			return forge.RerunChecksResult{}, errors.New("stale Actions workflow attempt")
		}
		if req.Progress == nil {
			return forge.RerunChecksResult{}, errors.New("github Actions rerun requires durable dispatch progress")
		}
		if exists && !target.NotDispatched {
			if uint64(run.GetRunAttempt()) > target.Attempt {
				target.Accepted = true
				if err := req.Progress(target); err != nil {
					return forge.RerunChecksResult{}, err
				}
				continue
			}
			uncertain = errors.New("github Actions rerun outcome is uncertain; waiting for a newer workflow attempt")
			continue
		}
		// A prior/external rerun can be visible before any new check-run ID.
		if uint64(run.GetRunAttempt()) > originalAttempts[id] {
			if exists {
				target.Accepted, target.NotDispatched = true, false
				if err := req.Progress(target); err != nil {
					return forge.RerunChecksResult{}, err
				}
			}
			continue
		}
		if run.GetStatus() != "completed" {
			uncertain = errors.New("inconsistent Actions workflow status at the original attempt; waiting for a fresh observation")
			continue
		}
		target = forge.RerunTarget{WorkflowRunID: id, Attempt: uint64(run.GetRunAttempt())}
		if err := req.Progress(target); err != nil {
			return forge.RerunChecksResult{}, err
		}
		if _, err := d.client.Actions.RerunFailedJobsByID(ctx, owner, name, int64(id)); err != nil {
			if isUnsupportedRerunError(err) {
				var response *gh.ErrorResponse
				errors.As(err, &response)
				return unsupported("GitHub Actions rerun refused: " + d.app.sanitizer.Sanitize(response.Message))
			}
			if definitelyNotDispatched(err) {
				target.NotDispatched = true
				if progressErr := req.Progress(target); progressErr != nil {
					return forge.RerunChecksResult{}, progressErr
				}
			}
			// Even a 5xx may follow acceptance. Leave the committed intent and
			// reconcile it on the next execution instead of repeating the POST.
			uncertain = safeError(err)
			continue
		}
		target.Accepted = true
		if err := req.Progress(target); err != nil {
			return forge.RerunChecksResult{}, err
		}
	}
	if uncertain != nil {
		return forge.RerunChecksResult{}, uncertain
	}
	return forge.RerunChecksResult{Detail: "Actions rerun requested or already observed"}, nil
}

func nextCheckRunAttempt(run *gh.CheckRun) uint64 {
	if explicit := checkRunAttemptFromURL(run); explicit > 0 {
		return explicit
	}
	// Keep the legacy observation stable at one. A duplicate API observation is
	// the same native run, not evidence of a new attempt.
	return 1
}

func checkRunAttemptFromURL(run *gh.CheckRun) uint64 {
	for _, raw := range []string{run.GetDetailsURL(), run.GetHTMLURL(), run.GetURL()} {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] != "attempts" {
				continue
			}
			attempt, err := strconv.ParseUint(parts[i+1], 10, 64)
			if err == nil && attempt > 0 {
				return attempt
			}
		}
	}
	return 0
}

const githubActionsAppID int64 = 15368

func workflowRunID(run *gh.CheckRun, gitBase *url.URL, owner, name string) int64 {
	if run.GetApp().GetID() != githubActionsAppID || gitBase == nil {
		return 0
	}
	for _, raw := range []string{run.GetDetailsURL(), run.GetHTMLURL(), run.GetURL()} {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, gitBase.Host) {
			continue
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 5 || parts[0] != owner || parts[1] != name || parts[2] != "actions" || parts[3] != "runs" {
			continue
		}
		runID, err := strconv.ParseInt(parts[4], 10, 64)
		if err == nil && runID > 0 {
			return runID
		}
	}
	return 0
}

func rerunnableCheckState(state forge.CheckState) bool {
	switch state {
	case forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral:
		return true
	default:
		return false
	}
}

func effectiveGitHubAttempt(attempt uint64) uint64 {
	if attempt == 0 {
		return 1
	}
	return attempt
}

func newerGitHubCheck(candidate, current forge.CheckRun) bool {
	if candidate.ID != current.ID {
		return candidate.ID > current.ID
	}
	return effectiveGitHubAttempt(candidate.Attempt) > effectiveGitHubAttempt(current.Attempt)
}

func isUnsupportedRerunError(err error) bool {
	var response *gh.ErrorResponse
	return errors.As(err, &response) && response.Response != nil && response.Response.StatusCode == 403 && strings.Contains(strings.ToLower(response.Message), "resource not accessible by integration")
}

// CheckRunsForRetry joins each check ID to its actual Actions job attempt.
// A workflow's current run_attempt must never be assigned to retained old jobs.
// Legacy/default-off CheckRuns performs no additional Actions API reads.
func (d *Driver) CheckRunsForRetry(ctx context.Context, repo forge.RepoRef, sha string, workflows []uint64) ([]forge.CheckRun, error) {
	checks, err := d.CheckRuns(ctx, repo, sha)
	if err != nil {
		return nil, err
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	needed := make(map[uint64]bool)
	for _, id := range workflows {
		needed[id] = true
	}
	for _, check := range checks {
		if rerunnableCheckState(check.State) {
			needed[check.WorkflowRunID] = true
		}
	}
	attempts := make(map[uint64]map[uint64]uint64)
	for _, check := range checks {
		id := check.WorkflowRunID
		if id == 0 || !needed[id] || attempts[id] != nil {
			continue
		}
		attempts[id] = make(map[uint64]uint64)
		for page := 1; page != 0; {
			jobs, res, err := d.client.Actions.ListWorkflowJobs(ctx, owner, name, int64(id), &gh.ListWorkflowJobsOptions{Filter: "all", ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
			if err != nil {
				if isUnsupportedRerunError(err) {
					// Return the untouched plain observation: no partial job enrichment.
					for i := range checks {
						if checks[i].Source == "app:15368" {
							checks[i].RetryUnsupported = "Actions permission not granted; failed-check reruns unavailable"
						}
					}
					return checks, nil
				}
				return nil, safeError(err)
			}
			for _, job := range jobs.Jobs {
				u, err := url.Parse(job.GetCheckRunURL())
				if err != nil {
					continue
				}
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) < 2 || parts[len(parts)-2] != "check-runs" {
					continue
				}
				checkID, err := strconv.ParseUint(parts[len(parts)-1], 10, 64)
				if err == nil && job.GetRunAttempt() > 0 {
					attempts[id][checkID] = uint64(job.GetRunAttempt())
				}
			}
			page = res.NextPage
		}
	}
	for i := range checks {
		if attempt := attempts[checks[i].WorkflowRunID][checks[i].ID]; attempt > 0 {
			checks[i].Attempt = attempt
		}
	}
	return checks, nil
}

func actionsCheckURL(run *gh.CheckRun, base *url.URL, owner, name string, workflowID uint64) string {
	prefix := fmt.Sprintf("/%s/%s/actions/runs/%d", owner, name, workflowID)
	for _, raw := range []string{run.GetDetailsURL(), run.GetHTMLURL()} {
		u, err := url.Parse(raw)
		if err == nil && base != nil && strings.EqualFold(u.Host, base.Host) && (u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/")) {
			u.RawQuery, u.Fragment, u.User = "", "", nil
			return u.String()
		}
	}
	return ""
}

func definitelyNotDispatched(err error) bool {
	var notSent *requestNotSentError
	var rate *RateLimitError
	var primary *gh.RateLimitError
	var secondary *gh.AbuseRateLimitError
	return errors.As(err, &notSent) || errors.As(err, &rate) || errors.As(err, &primary) || errors.As(err, &secondary)
}
