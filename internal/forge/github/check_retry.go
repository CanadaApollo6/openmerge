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

type githubRerunRecord struct {
	fingerprint string
	result      forge.RerunChecksResult
}

type rerunCandidate struct {
	run         *gh.CheckRun
	observed    forge.CheckRun
	workflowRun int64
}

// RerunFailedChecks rereads the exact check identity before issuing a narrow
// provider request. CheckRuns intentionally does not call the Actions API;
// this capability is opt-in and only uses an Actions endpoint when a requested
// check exposes a validated Actions workflow URL.
func (d *Driver) RerunFailedChecks(ctx context.Context, repo forge.RepoRef, req forge.RerunChecksRequest) (forge.RerunChecksResult, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.RerunChecksResult{}, err
	}
	if req.TestedSHA == "" || req.TestRef == "" || req.Key == "" || len(req.Runs) == 0 {
		return forge.RerunChecksResult{}, errors.New("tested SHA, test ref, key, and failed runs are required")
	}
	fingerprint := githubRerunFingerprint(req)
	d.writes.Lock()
	defer d.writes.Unlock()
	if d.rerunKeys == nil {
		d.rerunKeys = make(map[string]githubRerunRecord)
	}
	cacheKey := repo.Path + "\x00" + req.TestRef + "\x00" + req.Key
	if prior, ok := d.rerunKeys[cacheKey]; ok {
		if prior.fingerprint != fingerprint {
			return forge.RerunChecksResult{}, fmt.Errorf("rerun key %q was already used for another request", req.Key)
		}
		return prior.result, nil
	}

	for _, requested := range req.Runs {
		if strings.HasPrefix(requested.Source, "status:") {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "commit statuses cannot be rerun"}), nil
		}
		if !rerunnableCheckState(requested.State) {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{
				Unsupported: true,
				Detail:      fmt.Sprintf("check %q is not rerunnable in state %q", requested.Name, requested.State),
			}), nil
		}
		if requested.Name == "" || requested.Source == "" {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check provenance is unavailable"}), nil
		}
		if requested.ID == 0 {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check run has no native ID"}), nil
		}
		if requested.HeadBranch != "" && requested.HeadBranch != req.TestRef {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check is not bound to the requested test ref"}), nil
		}
		if !strings.HasPrefix(requested.Source, "app:") || requested.Source == "app:0" {
			return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check producer cannot be rerequested"}), nil
		}
	}

	candidates, err := d.rerunCandidates(ctx, owner, name, req.TestedSHA)
	if err != nil {
		return forge.RerunChecksResult{}, err
	}

	type plan struct {
		candidate rerunCandidate
		dispatch  bool
	}
	plans := make([]plan, 0, len(req.Runs))
	unsupported := false
	for _, requested := range req.Runs {
		requestedAttempt := effectiveGitHubAttempt(requested.Attempt)
		var exact, newest *rerunCandidate
		for i := range candidates {
			candidate := &candidates[i]
			if candidate.observed.Name != requested.Name || candidate.observed.Source != requested.Source || candidate.observed.HeadBranch != req.TestRef {
				continue
			}
			if newest == nil || newerGitHubCheck(candidate.observed, newest.observed) {
				newest = candidate
			}
			if candidate.observed.ID == requested.ID && effectiveGitHubAttempt(candidate.observed.Attempt) == requestedAttempt {
				exact = candidate
			}
		}
		if exact == nil {
			if newest != nil && newerGitHubCheck(newest.observed, forge.CheckRun{ID: requested.ID, Attempt: requestedAttempt}) {
				// The original may remain failed in the API. A newer run is
				// sufficient evidence that a previous request took effect.
				plans = append(plans, plan{})
				continue
			}
			unsupported = true
			continue
		}
		if newest != nil && (newest.observed.ID != exact.observed.ID || effectiveGitHubAttempt(newest.observed.Attempt) != effectiveGitHubAttempt(exact.observed.Attempt)) {
			plans = append(plans, plan{})
			continue
		}
		if !rerunnableCheckState(exact.observed.State) {
			plans = append(plans, plan{})
			continue
		}
		plans = append(plans, plan{candidate: *exact, dispatch: true})
	}
	if unsupported {
		return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "exact failed check is no longer observable"}), nil
	}

	actions := make(map[int64]struct{})
	checkRuns := make(map[int64]struct{})
	for _, item := range plans {
		if !item.dispatch {
			continue
		}
		if item.candidate.workflowRun > 0 {
			actions[item.candidate.workflowRun] = struct{}{}
		} else {
			checkRuns[int64(item.candidate.observed.ID)] = struct{}{}
		}
	}
	if len(actions) == 0 && len(checkRuns) == 0 {
		return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Detail: "rerun already observed"}), nil
	}
	for runID := range actions {
		if _, err := d.client.Actions.RerunFailedJobsByID(ctx, owner, name, runID); err != nil {
			normalized := safeError(err)
			if isUnsupportedRerunError(normalized) {
				return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "GitHub Actions reruns are not permitted for this installation"}), nil
			}
			return forge.RerunChecksResult{}, normalized
		}
	}
	for checkID := range checkRuns {
		if _, err := d.client.Checks.ReRequestCheckRun(ctx, owner, name, checkID); err != nil {
			normalized := safeError(err)
			if isUnsupportedRerunError(normalized) {
				return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Unsupported: true, Detail: "check reruns are not permitted for this installation"}), nil
			}
			return forge.RerunChecksResult{}, normalized
		}
	}
	return d.recordRerun(cacheKey, fingerprint, forge.RerunChecksResult{Detail: "rerun requested"}), nil
}

func (d *Driver) recordRerun(key, fingerprint string, result forge.RerunChecksResult) forge.RerunChecksResult {
	if _, exists := d.rerunKeys[key]; !exists {
		d.rerunOrder = append(d.rerunOrder, key)
		if len(d.rerunOrder) > 1024 {
			oldest := d.rerunOrder[0]
			d.rerunOrder = d.rerunOrder[1:]
			delete(d.rerunKeys, oldest)
		}
	}
	d.rerunKeys[key] = githubRerunRecord{fingerprint: fingerprint, result: result}
	return result
}

func (d *Driver) rerunCandidates(ctx context.Context, owner, name, sha string) ([]rerunCandidate, error) {
	runs, err := d.checkRuns(ctx, owner, name, sha)
	if err != nil {
		return nil, err
	}
	suites := make(map[int64]*gh.CheckSuite)
	for page := 1; page != 0; {
		list, res, err := d.client.Checks.ListCheckSuitesForRef(ctx, owner, name, sha, &gh.ListCheckSuiteOptions{ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, safeError(err)
		}
		for _, suite := range list.CheckSuites {
			suites[suite.GetID()] = suite
		}
		page = res.NextPage
	}
	result := make([]rerunCandidate, 0, len(runs))
	for _, run := range runs {
		if d.own(run) || run.GetHeadSHA() != sha {
			continue
		}
		id := uint64(max(run.GetID(), 0))
		source := "app:" + strconv.FormatInt(run.GetApp().GetID(), 10)
		suite := suites[run.GetCheckSuite().GetID()]
		state := checkState(run.GetStatus(), run.GetConclusion())
		if run.GetCheckSuite().GetID() <= 0 || suite.GetStatus() != "completed" || run.GetApp().GetID() <= 0 {
			state = forge.Pending
		}
		attempt := nextCheckRunAttempt(run)
		result = append(result, rerunCandidate{
			run:         run,
			observed:    forge.CheckRun{ID: id, Attempt: attempt, Name: run.GetName(), Source: source, State: state, HeadBranch: suite.GetHeadBranch()},
			workflowRun: workflowRunID(run, d.app.git, owner, name),
		})
	}
	return result, nil
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

func githubRerunFingerprint(req forge.RerunChecksRequest) string {
	runs := slices.Clone(req.Runs)
	for i := range runs {
		runs[i].Attempt = effectiveGitHubAttempt(runs[i].Attempt)
	}
	slices.SortFunc(runs, func(a, b forge.CheckRun) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		if a.Source != b.Source {
			return strings.Compare(a.Source, b.Source)
		}
		if a.ID != b.ID {
			if a.ID < b.ID {
				return -1
			}
			return 1
		}
		if a.Attempt < b.Attempt {
			return -1
		}
		if a.Attempt > b.Attempt {
			return 1
		}
		return strings.Compare(a.HeadBranch, b.HeadBranch)
	})
	return fmt.Sprintf("%s\x00%s\x00%#v", req.TestedSHA, req.TestRef, runs)
}

func isUnsupportedRerunError(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	return api.StatusCode == 403 || api.StatusCode == 404 || api.StatusCode == 422
}
