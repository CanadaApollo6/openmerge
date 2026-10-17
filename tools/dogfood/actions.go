package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/google/go-github/v93/github"
)

// Actions separates measurements from transport; fixtures never contact GitHub.
type Actions interface {
	List(context.Context, string) ([]*github.WorkflowRun, error)
	Attempt(context.Context, int64, int) (*github.WorkflowRun, error)
}

// AuditSource adds independent forge history to the Actions evidence.
type AuditSource interface {
	Actions
	BaseTip(context.Context, string) (string, error)
	Ancestor(context.Context, string, string) (bool, error)
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

func (a githubActions) BaseTip(ctx context.Context, branch string) (string, error) {
	b, _, err := a.client.Repositories.GetBranch(ctx, a.owner, a.repo, branch, 0)
	if err != nil {
		return "", fmt.Errorf("read base branch tip: %w", err)
	}
	sha := b.GetCommit().GetSHA()
	if !validSHA(sha) {
		return "", fmt.Errorf("base branch has no valid commit SHA")
	}
	return sha, nil
}
func (a githubActions) Ancestor(ctx context.Context, landed, tip string) (bool, error) {
	if !validSHA(landed) || !validSHA(tip) {
		return false, fmt.Errorf("invalid ancestry commit SHA")
	}
	comparison, _, err := a.client.Repositories.CompareCommits(ctx, a.owner, a.repo, landed, tip, &github.ListOptions{PerPage: 1})
	if err != nil {
		return false, fmt.Errorf("compare landing with base tip: %w", err)
	}
	switch comparison.GetStatus() {
	case "ahead", "identical":
		return true, nil
	case "behind", "diverged":
		return false, nil
	default:
		return false, fmt.Errorf("missing or unknown comparison status %q", comparison.GetStatus())
	}
}

type Attempt struct {
	ID          int64     `json:"run_id"`
	Number      int       `json:"run_attempt"`
	At          time.Time `json:"at"`
	SHA         string    `json:"head_sha"`
	Branch      string    `json:"branch"`
	Conclusion  string    `json:"conclusion"`
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
}

func queueCI(r *github.WorkflowRun) bool {
	return r.GetPath() == ".github/workflows/ci.yml" && strings.HasPrefix(r.GetHeadBranch(), "openmerge/queue/")
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
			run, err := a.Attempt(ctx, r.GetID(), n)
			if err != nil {
				return nil, fmt.Errorf("run %d attempt %d: %w", r.GetID(), n, err)
			}
			if run.GetID() != r.GetID() || run.GetRunAttempt() != n || run.GetHeadSHA() != r.GetHeadSHA() || run.GetHeadBranch() != r.GetHeadBranch() || !queueCI(run) {
				return nil, fmt.Errorf("run %d attempt %d has inconsistent Actions metadata", r.GetID(), n)
			}
			result = append(result, run)
		}
	}
	return result, nil
}
func countRuns(ctx context.Context, a Actions, base string, batches []string) ([]Attempt, error) {
	runs, err := a.List(ctx, "")
	if err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	for _, id := range batches {
		refs["openmerge/queue/"+base+"/"+id] = true
	}
	var own []*github.WorkflowRun
	for _, r := range runs {
		if refs[r.GetHeadBranch()] {
			own = append(own, r)
		}
	}
	expanded, err := attempts(ctx, a, own)
	if err != nil {
		return nil, err
	}
	var result []Attempt
	for _, r := range expanded {
		if !refs[r.GetHeadBranch()] {
			continue
		}
		at := r.GetRunStartedAt().Time
		// Queued attempts have no start yet; creation is enough to count them.
		if at.IsZero() {
			at = r.GetCreatedAt().Time
		}
		if at.IsZero() {
			return nil, fmt.Errorf("run %d attempt %d has no timestamp", r.GetID(), r.GetRunAttempt())
		}
		if r.GetStatus() == "" {
			return nil, fmt.Errorf("run %d has no status", r.GetID())
		}
		done := time.Time{}
		if r.GetStatus() == "completed" {
			done = r.GetUpdatedAt().Time
			if done.IsZero() || done.Before(at) {
				return nil, fmt.Errorf("run %d attempt %d has invalid completion timestamp", r.GetID(), r.GetRunAttempt())
			}
		}
		result = append(result, Attempt{ID: r.GetID(), Number: r.GetRunAttempt(), At: at, SHA: r.GetHeadSHA(), Branch: r.GetHeadBranch(), Conclusion: r.GetConclusion(), Status: r.GetStatus(), CompletedAt: done})
	}
	return result, nil
}

func finishDrain(r *Report, j store.Journal) {
	end := r.QueueDrainedAt
	complete := !end.IsZero() && r.Pending == 0
	for _, run := range r.Runs {
		if run.Status != "completed" || run.CompletedAt.After(r.ReportAt) {
			complete = false
		}
		end = maxTime(end, run.CompletedAt)
	}
	r.RunAttempts = len(r.Runs)
	if r.Landed > 0 {
		r.RunsPerMerge = float64(r.RunAttempts) / float64(r.Landed)
	}
	r.DrainComplete = complete
	if complete {
		r.DrainEnd = maxTime(r.Window.To.Time, end)
		r.DrainSeconds = r.DrainEnd.Sub(r.Window.To.Time).Seconds()
	} else {
		r.DrainSeconds = r.ReportAt.Sub(r.Window.To.Time).Seconds()
		r.Warnings = append(r.Warnings, "drain incomplete at report time: cohort/queue or period CI still outstanding")
		end = r.ReportAt
	}
	if r.NextConfig != nil && (!complete || r.NextConfig.Time.Before(r.DrainEnd)) {
		r.Warnings = append(r.Warnings, "next configuration was applied before period drain completed")
	}
	r.HeldArrivals = heldArrivals(j, r.Window.To.Time, end, r.ReportAt)
}
