package cidoctor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// DefaultActionsAPIBudget bounds metadata/page/file requests, including retries.
// App identity and installation-token acquisition use the existing auth cache.
const DefaultActionsAPIBudget = 500

var (
	errActionsBudget     = errors.New("Actions report API call budget exhausted")
	errActionsIncomplete = errors.New("incomplete Actions pagination")
	errReadScope         = errors.New("CI doctor reads only workflow files and OpenMerge configuration")
)

// reportReadable limits Contents reads to the dependency-graph evidence the jobs
// API omits: the run's workflow file and the OpenMerge policy at the run's SHA.
func reportReadable(path string) bool {
	if slices.Contains(config.Paths, path) {
		return true
	}
	name, ok := strings.CutPrefix(path, ".github/workflows/")
	return ok && name != "" && !strings.ContainsAny(name, "/\\") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml"))
}

// liveRead charges every attempted data request. Retry only reads, at most three
// times, preserving cancellation and the installation transport's quota floors.
func liveRead[T any](ctx context.Context, remaining *int, read func() (T, error)) (T, error) {
	var value T
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return value, err
		}
		if *remaining == 0 {
			return value, errActionsBudget
		}
		*remaining--
		v, err := read()
		if err == nil {
			return v, nil
		}
		value = v
		var status interface{ HTTPStatusCode() int }
		var rate interface{ RetryAfter() time.Time }
		if errors.Is(err, forge.ErrActionsUnavailable) || errors.Is(err, forge.ErrFileNotFound) || errors.As(err, &rate) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &status) && status.HTTPStatusCode() < 500 || attempt == 2 {
			return value, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable")
}

// ReadActions collects only journal-identified queue runs and default-branch
// baselines. It never discovers queue runs by SHA or reads PR runs, logs or artifacts.
func ReadActions(ctx context.Context, reader forge.ActionsReader, j store.Journal, w Window, budget int) (Actions, error) {
	if budget <= 0 {
		return Actions{}, errors.New("Actions API budget must be positive")
	}
	ctx = forge.WithRequestClass(ctx, forge.ReportingRequest)
	repo := forge.RepoRef{Path: j.Key.Repository}
	result := Actions{Version: CIDoctorVersion, Repository: j.Key.Repository, DefaultBranch: j.Key.BaseBranch, Runs: []Run{}}
	gap := func(id uint64, err error) bool {
		code := ""
		var rate interface{ RetryAfter() time.Time }
		switch {
		case errors.Is(err, errActionsBudget):
			code = "actions-budget"
		case errors.Is(err, errActionsIncomplete):
			code = "actions-data"
		case errors.Is(err, forge.ErrActionsUnavailable):
			code = "actions-permission"
		case errors.As(err, &rate):
			code = "actions-rate-limit"
		case readDenied(err):
			code = "actions-access"
		}
		if code == "" {
			return false
		}
		result.Gaps = append(result.Gaps, Gap{RunID: id, Code: code, Detail: err.Error()})
		return true
	}
	branch, err := liveRead(ctx, &budget, func() (string, error) { return reader.DefaultBranch(ctx, repo) })
	if err != nil {
		if gap(0, err) {
			return result, nil
		}
		return Actions{}, fmt.Errorf("CI doctor reading default branch: %w", err)
	}
	result.DefaultBranch = branch
	pushes := journalPushes(j, w)
	ids := []uint64{}
	for _, p := range pushes {
		for id := range p.checks {
			ids = append(ids, id)
		}
		for retry := range p.retries {
			ids = append(ids, retry.id)
		}
	}
	ids = unique(ids)
	files := map[string][]byte{}
	readFile := func(sha, path string) ([]byte, error) {
		key := sha + ":" + path
		if data, ok := files[key]; ok {
			return data, nil
		}
		if !reportReadable(path) {
			return nil, fmt.Errorf("%w: %q", errReadScope, path)
		}
		data, err := liveRead(ctx, &budget, func() ([]byte, error) { return reader.ReadFile(ctx, repo, sha, path) })
		if err == nil {
			files[key] = data
		}
		return data, err
	}
	loadJobs := func(r Run) (Run, error) {
		r.Jobs, r.JobsComplete, r.Graph = nil, false, nil
		total := -1
		for page := 1; ; {
			jobs, e := liveRead(ctx, &budget, func() (forge.ActionsJobsPage, error) { return reader.ActionsJobs(ctx, repo, r.ID, r.Attempt, page) })
			if e != nil {
				return r, e
			}
			if total >= 0 && total != jobs.Total {
				return r, fmt.Errorf("%w: jobs total changed", errActionsIncomplete)
			}
			total = jobs.Total
			r.Jobs = append(r.Jobs, jobs.Jobs...)
			if jobs.NextPage == 0 {
				if len(r.Jobs) != total {
					return r, fmt.Errorf("%w: jobs count differs from total", errActionsIncomplete)
				}
				r.JobsComplete = true
				return r, nil
			}
			if jobs.NextPage <= page || len(jobs.Jobs) == 0 || len(r.Jobs) >= total {
				return r, fmt.Errorf("%w: invalid jobs next page", errActionsIncomplete)
			}
			page = jobs.NextPage
		}
	}
	for _, id := range ids {
		r, err := liveRead(ctx, &budget, func() (Run, error) { return reader.ActionsRun(ctx, repo, id) })
		if err != nil {
			if gap(id, err) {
				continue
			}
			return Actions{}, fmt.Errorf("CI doctor reading Actions run %d: %w", id, err)
		}
		matches := 0
		for _, p := range pushes {
			if p.sha == r.HeadSHA && p.ref == r.HeadBranch && (p.checks[id] || len(p.checks) == 0) {
				matches++
			}
		}
		if r.ID != id || matches != 1 || r.Event != "push" || r.Attempt == 0 {
			result.Gaps = append(result.Gaps, Gap{RunID: id, Code: "run-identity", Detail: "Actions run does not match one journal push"})
			continue
		}
		latest := r.Attempt
		for attempt := uint64(1); attempt <= latest; attempt++ {
			r.Attempt = attempt
			loaded, err := loadJobs(r)
			if err != nil {
				if gap(id, err) {
					break
				}
				return Actions{}, fmt.Errorf("CI doctor reading Actions jobs for run %d attempt %d: %w", id, attempt, err)
			}
			workflow, err := readFile(r.HeadSHA, r.Path)
			if err == nil {
				loaded.Graph = workflowGraph(loaded, workflow)
			}
			if err != nil && !errors.Is(err, forge.ErrFileNotFound) && !readDenied(err) {
				if !gap(id, err) {
					return Actions{}, fmt.Errorf("CI doctor reading workflow for run %d: %w", id, err)
				}
			}
			if loaded.Graph != nil {
				// Required-check policy is read at the same revision, never from the live
				// branch tip. Unknown policy suppresses masked-skip, not timing evidence.
				loaded.Graph.Required, err = requiredChecks(readFile, r.HeadSHA, j.Key.BaseBranch, j.Key.Lane, branch)
				if err != nil && !errors.Is(err, forge.ErrFileNotFound) && !readDenied(err) {
					if !gap(id, err) {
						return Actions{}, fmt.Errorf("CI doctor reading policy for run %d: %w", id, err)
					}
				}
			}
			result.Runs = append(result.Runs, loaded)
		}
	}
	// Buffer the complete baseline inventory before using it. GitHub caps filtered
	// run searches at 1000; a truncated list cannot silently bias the comparison.
	var baseline []Run
	total := -1
	for page := 1; ; {
		runs, err := liveRead(ctx, &budget, func() (forge.ActionsRunsPage, error) {
			return reader.DefaultBranchActions(ctx, repo, branch, w.Since, w.Until, page)
		})
		if err != nil {
			if gap(0, err) {
				return result, nil
			}
			return Actions{}, fmt.Errorf("CI doctor listing default-branch Actions runs: %w", err)
		}
		if total >= 0 && total != runs.Total {
			result.Gaps = append(result.Gaps, Gap{Code: "baseline-data", Detail: "default-branch run total changed during pagination"})
			return result, nil
		}
		total = runs.Total
		baseline = append(baseline, runs.Runs...)
		if runs.NextPage == 0 {
			if len(baseline) != total {
				result.Gaps = append(result.Gaps, Gap{Code: "baseline-data", Detail: "incomplete default-branch run listing (GitHub search limit)"})
				return result, nil
			}
			break
		}
		if runs.NextPage <= page || len(runs.Runs) == 0 || len(baseline) >= total {
			result.Gaps = append(result.Gaps, Gap{Code: "baseline-data", Detail: "invalid default-branch run pagination"})
			return result, nil
		}
		page = runs.NextPage
	}
	slices.SortFunc(baseline, func(a, b Run) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	// A repeated entry across pages can stand in for an omitted run while the
	// count still matches Total, so the inventory is only complete with unique IDs.
	for i := 1; i < len(baseline); i++ {
		if baseline[i].ID == baseline[i-1].ID {
			result.Gaps = append(result.Gaps, Gap{Code: "baseline-data", Detail: "duplicate default-branch run in pagination"})
			return result, nil
		}
	}
	for _, r := range baseline {
		if r.HeadBranch != branch || r.Event != "push" || r.CreatedAt.Before(w.Since) || !r.CreatedAt.Before(w.Until) {
			continue
		}
		r.Attempt = 1
		loaded, err := loadJobs(r)
		if err != nil {
			if gap(r.ID, err) {
				continue
			}
			return Actions{}, fmt.Errorf("CI doctor reading baseline jobs for run %d: %w", r.ID, err)
		}
		result.Runs = append(result.Runs, loaded)
	}
	return result, nil
}

func readDenied(err error) bool {
	if errors.Is(err, errReadScope) {
		return true
	}
	var status interface{ HTTPStatusCode() int }
	return errors.As(err, &status) && (status.HTTPStatusCode() == 403 || status.HTTPStatusCode() == 404)
}
