package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// Actions separates measurements from transport; fixtures never contact GitHub.
type Actions interface {
	List(context.Context, string) ([]*github.WorkflowRun, error)
	Attempt(context.Context, int64, int) (*github.WorkflowRun, error)
}
type githubActions struct {
	client      *github.Client
	owner, repo string
}

func (a githubActions) List(ctx context.Context, sha string) ([]*github.WorkflowRun, error) {
	opts := &github.ListWorkflowRunsOptions{HeadSHA: sha, ListOptions: github.ListOptions{PerPage: 100, Page: 1}}
	var all []*github.WorkflowRun
	total := 0
	for {
		runs, resp, err := a.client.Actions.ListRepositoryWorkflowRuns(ctx, a.owner, a.repo, opts)
		if err != nil {
			return nil, fmt.Errorf("list Actions runs: %w", err)
		}
		total = max(total, runs.GetTotalCount())
		if sha != "" && total > 1000 {
			return nil, fmt.Errorf("Actions head_sha search exceeds GitHub's 1000-run limit; cannot certify completeness")
		}
		all = append(all, runs.WorkflowRuns...)
		if resp.NextPage == 0 {
			break
		}
		if resp.NextPage <= opts.Page {
			return nil, fmt.Errorf("Actions pagination did not advance")
		}
		opts.Page = resp.NextPage
	}
	ids := map[int64]bool{}
	for _, r := range all {
		ids[r.GetID()] = true
	}
	if len(ids) < total {
		return nil, fmt.Errorf("Actions returned %d distinct runs of %d; retry after concurrent updates or investigate API truncation", len(ids), total)
	}
	return all, nil
}
func (a githubActions) Attempt(ctx context.Context, id int64, n int) (*github.WorkflowRun, error) {
	run, _, err := a.client.Actions.GetWorkflowRunAttempt(ctx, a.owner, a.repo, id, n, nil)
	return run, err
}

type Attempt struct {
	ID         int64     `json:"run_id"`
	Number     int       `json:"run_attempt"`
	At         time.Time `json:"at"`
	SHA        string    `json:"head_sha"`
	Branch     string    `json:"branch"`
	Conclusion string    `json:"conclusion"`
}

func queueCI(r *github.WorkflowRun) bool {
	return r.GetName() == "CI" && strings.HasPrefix(r.GetHeadBranch(), "openmerge/queue/")
}

func attempts(ctx context.Context, a Actions, runs []*github.WorkflowRun) ([]*github.WorkflowRun, error) {
	seen := map[[2]int64]bool{}
	var result []*github.WorkflowRun
	for _, r := range runs {
		if !queueCI(r) {
			continue
		}
		if r.GetID() <= 0 || r.GetRunAttempt() < 1 {
			return nil, fmt.Errorf("CI run has no run ID or attempt number")
		}
		for n := 1; n <= r.GetRunAttempt(); n++ {
			key := [2]int64{r.GetID(), int64(n)}
			if seen[key] {
				continue
			}
			seen[key] = true
			run := r
			if r.GetRunAttempt() > 1 {
				var err error
				run, err = a.Attempt(ctx, r.GetID(), n)
				if err != nil {
					return nil, fmt.Errorf("run %d attempt %d: %w", r.GetID(), n, err)
				}
			}
			if run.GetID() != r.GetID() || run.GetRunAttempt() != n || run.GetHeadSHA() != r.GetHeadSHA() || !queueCI(run) {
				return nil, fmt.Errorf("run %d attempt %d has inconsistent Actions metadata", r.GetID(), n)
			}
			result = append(result, run)
		}
	}
	return result, nil
}
func countRuns(ctx context.Context, a Actions, w Window) ([]Attempt, error) {
	runs, err := a.List(ctx, "")
	if err != nil {
		return nil, err
	}
	var candidates []*github.WorkflowRun
	for _, r := range runs {
		if r.GetCreatedAt().IsZero() && queueCI(r) {
			return nil, fmt.Errorf("run %d has no creation time", r.GetID())
		}
		if r.GetCreatedAt().Before(w.To.Time) {
			candidates = append(candidates, r)
		}
	}
	expanded, err := attempts(ctx, a, candidates)
	if err != nil {
		return nil, err
	}
	var result []Attempt
	for _, r := range expanded {
		at := r.GetCreatedAt().Time
		if r.GetRunAttempt() > 1 {
			at = r.GetRunStartedAt().Time
		}
		if at.IsZero() {
			return nil, fmt.Errorf("run %d attempt %d has no attempt timestamp; retry when the rerun has started", r.GetID(), r.GetRunAttempt())
		}
		if !at.Before(w.From.Time) && at.Before(w.To.Time) {
			result = append(result, Attempt{ID: r.GetID(), Number: r.GetRunAttempt(), At: at, SHA: r.GetHeadSHA(), Branch: r.GetHeadBranch(), Conclusion: r.GetConclusion()})
		}
	}
	return result, nil
}
