package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.ActionsReader = (*Driver)(nil)

// actionsGet shares installation auth, conditional GETs, and reporting quota.
func (d *Driver) actionsGet(ctx context.Context, endpoint string, result any) (int, error) {
	ctx = forge.WithRequestClass(ctx, forge.ReportingRequest)
	if _, err := d.app.token(ctx, d.installation); err != nil {
		return 0, err
	}
	d.app.mu.Lock()
	entry := d.app.tokens[d.installation]
	d.app.mu.Unlock()
	entry.mu.Lock()
	grant := entry.permissions["actions"]
	entry.mu.Unlock()
	if grant != "read" && grant != "write" {
		return 0, forge.ErrActionsUnavailable
	}
	req, err := d.client.NewRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, safeError(err)
	}
	response, err := d.client.Do(req, result)
	if err != nil {
		err = safeError(err)
		var api *APIError
		if errors.As(err, &api) && (api.StatusCode == 403 || api.StatusCode == 404) {
			return 0, forge.ErrActionsUnavailable
		}
		return 0, err
	}
	return response.NextPage, nil
}

// ActionsRun reads one identified workflow run.
func (d *Driver) ActionsRun(ctx context.Context, repo forge.RepoRef, id uint64) (forge.ActionsRun, error) {
	if _, _, err := splitRepo(repo); err != nil {
		return forge.ActionsRun{}, err
	}
	var run forge.ActionsRun
	_, err := d.actionsGet(ctx, fmt.Sprintf("repos/%s/actions/runs/%d", repo.Path, id), &run)
	return run, err
}

// ActionsJobs reads one page of attempt-specific jobs and steps.
func (d *Driver) ActionsJobs(ctx context.Context, repo forge.RepoRef, id, attempt uint64, page int) (forge.ActionsJobsPage, error) {
	if _, _, err := splitRepo(repo); err != nil {
		return forge.ActionsJobsPage{}, err
	}
	var result struct {
		Total int                `json:"total_count"`
		Jobs  []forge.ActionsJob `json:"jobs"`
	}
	next, err := d.actionsGet(ctx, fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", repo.Path, id, attempt, page), &result)
	return forge.ActionsJobsPage{Jobs: result.Jobs, Total: result.Total, NextPage: next}, err
}

// DefaultBranchActions lists one page of baseline push runs.
func (d *Driver) DefaultBranchActions(ctx context.Context, repo forge.RepoRef, branch string, since, until time.Time, page int) (forge.ActionsRunsPage, error) {
	if _, _, err := splitRepo(repo); err != nil {
		return forge.ActionsRunsPage{}, err
	}
	q := url.Values{"branch": {branch}, "event": {"push"}, "created": {since.UTC().Format(time.RFC3339Nano) + ".." + until.UTC().Format(time.RFC3339Nano)}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}
	var result struct {
		Total int                `json:"total_count"`
		Runs  []forge.ActionsRun `json:"workflow_runs"`
	}
	next, err := d.actionsGet(ctx, "repos/"+repo.Path+"/actions/runs?"+q.Encode(), &result)
	return forge.ActionsRunsPage{Runs: result.Runs, Total: result.Total, NextPage: next}, err
}
