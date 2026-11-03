package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type actionsState struct {
	runs  []forge.ActionsRun
	calls int
}

var _ forge.ActionsReader = (*Forge)(nil)

// SetActions records attempts and their jobs. Mutations of caller-owned data or
// returned pages cannot change the fake's evidence.
func (f *Forge) SetActions(repo forge.RepoRef, runs []forge.ActionsRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actions == nil {
		f.actions = map[forge.RepoRef]*actionsState{}
	}
	f.actions[repo] = &actionsState{runs: cloneActions(runs)}
}

func cloneActions[T any](in T) T {
	data, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}

func (f *Forge) actionsRead(ctx context.Context, repo forge.RepoRef, method string) (*actionsState, error) {
	if f.actions == nil {
		f.actions = map[forge.RepoRef]*actionsState{}
	}
	if f.actions[repo] == nil {
		f.actions[repo] = &actionsState{}
	}
	state := f.actions[repo]
	state.calls++
	return state, f.failure(ctx, method)
}

// ActionsReadCalls counts attempted Actions metadata/page reads.
func (f *Forge) ActionsReadCalls(repo forge.RepoRef) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actions[repo] == nil {
		return 0
	}
	return f.actions[repo].calls
}

// ActionsRun reads one identified workflow run.
func (f *Forge) ActionsRun(ctx context.Context, repo forge.RepoRef, id uint64) (forge.ActionsRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.actionsRead(ctx, repo, "ActionsRun")
	if err != nil {
		return forge.ActionsRun{}, err
	}
	var latest forge.ActionsRun
	for _, r := range state.runs {
		if r.ID == id && r.Attempt > latest.Attempt {
			latest = r
		}
	}
	if latest.ID == 0 {
		return latest, forge.ErrActionsUnavailable
	}
	latest.Jobs, latest.Graph, latest.JobsComplete = nil, nil, false
	return latest, nil
}

// ActionsJobs reads one page of attempt-specific jobs and steps.
func (f *Forge) ActionsJobs(ctx context.Context, repo forge.RepoRef, id, attempt uint64, page int) (forge.ActionsJobsPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.actionsRead(ctx, repo, "ActionsJobs")
	if err != nil {
		return forge.ActionsJobsPage{}, err
	}
	for _, r := range state.runs {
		if r.ID == id && r.Attempt == attempt {
			start, end, next := actionsPage(len(r.Jobs), page)
			return forge.ActionsJobsPage{Jobs: cloneActions(r.Jobs[start:end]), Total: len(r.Jobs), NextPage: next}, nil
		}
	}
	return forge.ActionsJobsPage{}, forge.ErrActionsUnavailable
}

// DefaultBranchActions lists one page of baseline push runs.
func (f *Forge) DefaultBranchActions(ctx context.Context, repo forge.RepoRef, branch string, since, until time.Time, page int) (forge.ActionsRunsPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state, err := f.actionsRead(ctx, repo, "DefaultBranchActions")
	if err != nil {
		return forge.ActionsRunsPage{}, err
	}
	latest := map[uint64]forge.ActionsRun{}
	for _, r := range state.runs {
		if r.HeadBranch == branch && r.Event == "push" && !r.CreatedAt.Before(since) && r.CreatedAt.Before(until) && r.Attempt > latest[r.ID].Attempt {
			latest[r.ID] = r
		}
	}
	runs := []forge.ActionsRun{}
	for _, r := range latest {
		r.Jobs, r.Graph, r.JobsComplete = nil, nil, false
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	start, end, next := actionsPage(len(runs), page)
	return forge.ActionsRunsPage{Runs: cloneActions(runs[start:end]), Total: len(runs), NextPage: next}, nil
}

func actionsPage(total, page int) (int, int, int) {
	if page < 1 {
		panic(fmt.Sprintf("invalid Actions page %d", page))
	}
	start := min((page-1)*100, total)
	end, next := min(start+100, total), 0
	if end < total {
		next = page + 1
	}
	return start, end, next
}
