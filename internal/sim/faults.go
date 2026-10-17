package sim

import (
	"container/heap"
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Builds get new batch keys after a failure, and uncertain lands must use
// ResolveLanding. Recovery therefore follows the logical operation, not just
// the original action key. A missed check read is also recovered by a terminal
// webhook followed by the definitive landing/dequeue event.
func faultScope(action queue.Action) string {
	switch a := action.(type) {
	case queue.BuildTestRef:
		return fmt.Sprintf("build/pr/%d", a.PRs[0].PR)
	case queue.RefreshChecks:
		return "checks/" + a.BatchID
	case queue.Land:
		return "landing/" + a.BatchID
	case queue.FastForwardResolveLanding:
		return "landing/" + a.BatchID
	case queue.ResolveLanding:
		return "landing/" + a.BatchID
	default:
		return action.Key()
	}
}

func (s *simulation) injectFault(scope string) {
	s.result.FaultsInjected++
	s.pendingFaults[scope]++
}

func (s *simulation) recoverFaults(scope string) {
	s.result.FaultsRecovered += s.pendingFaults[scope]
	delete(s.pendingFaults, scope)
}

var forgeMethods = []string{"CommitAuthors", "HeadBranchForDeletion", "DeleteBranch", "PullRequest", "CheckRuns", "DeleteRef", "ReportStatus", "MarkLanded", "Comment", "BuildTestRef", "Land", "EnsureFence", "AdvanceFence", "ReadFence", "ReadRef", "IsAncestor"}

type pushTimeout struct{}

func (*pushTimeout) Error() string   { return "simulated landing push timeout (request still in flight)" }
func (*pushTimeout) Timeout() bool   { return true }
func (*pushTimeout) Temporary() bool { return true }

// All executor-facing calls go through this boundary. Observer reads bypass it
// so an invariant cannot consume a fault intended for an executor operation.
type faultForge struct {
	*fake.Forge
	sim   *simulation
	scope string
	calls map[string]int
}

func (f *faultForge) before(method string) error {
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[method]++
	rate, ok := f.sim.cfg.Faults.Methods[method]
	if !ok {
		rate = f.sim.cfg.Faults.ForgeErrorRate
	}
	if f.sim.faults.Float64() < rate || f.calls[method] <= f.sim.cfg.Faults.FailFirst[method] {
		f.sim.injectFault(f.scope)
		f.sim.result.FaultsByMethod[method]++
		return errForge
	}
	return nil
}

func (f *faultForge) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest) (string, error) {
	defer f.sim.observeWrite()
	if err := f.before("Land"); err != nil {
		return "", err
	}
	if f.sim.faults.Float64() < f.sim.cfg.Faults.DelayedPushRate {
		req.PRs = append([]forge.BuildPR(nil), req.PRs...)
		f.sim.sequence++
		heap.Push(&f.sim.scheduled, scheduledEvent{at: f.sim.now.Add(f.sim.cfg.Faults.ReplayDelay), sequence: f.sim.sequence, kind: delayedPush, request: req})
		f.sim.result.DelayedPushes++
		f.sim.injectFault(f.scope)
		return "", errDelayed
	}
	return f.Forge.Land(ctx, repo, req)
}

func (f *faultForge) PullRequest(ctx context.Context, repo forge.RepoRef, number int) (forge.PullRequest, error) {
	if err := f.before("PullRequest"); err != nil {
		return forge.PullRequest{}, err
	}
	return f.Forge.PullRequest(ctx, repo, number)
}

func (f *faultForge) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	if err := f.before("CheckRuns"); err != nil {
		return nil, err
	}
	run := f.sim.tests[sha]
	if run != nil && run.done && run.success && !run.rerun && f.sim.cfg.Faults.CIRerunRate > 0 &&
		f.sim.state.Active != nil && f.scope == "landing/"+f.sim.state.Active.ID &&
		f.sim.faults.Float64() < f.sim.cfg.Faults.CIRerunRate {
		run.rerun, run.done = true, false
		if f.sim.cfg.CI == nil {
			f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 2, Name: "ci", State: forge.Pending}})
			f.sim.result.CIRuns++
		}
		f.sim.result.CIReruns++
		f.sim.injectFault("checks/" + f.sim.state.Active.ID)
		duration := f.sim.ciDuration()
		if f.sim.cfg.CI == nil {
			f.sim.schedule(f.sim.now.Add(duration), ciComplete, 0, sha)
		} else {
			f.sim.submitCI(sha, duration)
			f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: run.job.id, Name: "ci", State: forge.Pending}})
		}
	}
	return f.Forge.CheckRuns(ctx, repo, sha)
}

func (f *faultForge) DeleteRef(ctx context.Context, repo forge.RepoRef, ref string) error {
	defer f.sim.observeWrite()
	if err := f.before("DeleteRef"); err != nil {
		return err
	}
	return f.Forge.DeleteRef(ctx, repo, ref)
}

func (f *faultForge) ReportStatus(ctx context.Context, repo forge.RepoRef, pr int, head string, status forge.Status) error {
	defer f.sim.observeWrite()
	if err := f.before("ReportStatus"); err != nil {
		return err
	}
	return f.Forge.ReportStatus(ctx, repo, pr, head, status)
}

func (f *faultForge) MarkLanded(ctx context.Context, repo forge.RepoRef, number int, sha string) error {
	defer f.sim.observeWrite()
	if err := f.before("MarkLanded"); err != nil {
		return err
	}
	return f.Forge.MarkLanded(ctx, repo, number, sha)
}

func (f *faultForge) Comment(ctx context.Context, repo forge.RepoRef, pr int, key, body string) error {
	defer f.sim.observeWrite()
	if err := f.before("Comment"); err != nil {
		return err
	}
	return f.Forge.Comment(ctx, repo, pr, key, body)
}

func (f *faultForge) BuildTestRef(ctx context.Context, repo forge.RepoRef, req forge.BuildRequest) (string, error) {
	defer f.sim.observeWrite()
	if err := f.before("BuildTestRef"); err != nil {
		return "", err
	}
	return f.Forge.BuildTestRef(ctx, repo, req)
}

func (f *faultForge) EnsureFence(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	defer f.sim.observeWrite()
	if err := f.before("EnsureFence"); err != nil {
		return "", err
	}
	return f.Forge.EnsureFence(ctx, repo, ref)
}

func (f *faultForge) AdvanceFence(ctx context.Context, repo forge.RepoRef, ref, expected, next string) (string, error) {
	defer f.sim.observeWrite()
	if err := f.before("AdvanceFence"); err != nil {
		return "", err
	}
	return f.Forge.AdvanceFence(ctx, repo, ref, expected, next)
}

func (f *faultForge) ReadFence(ctx context.Context, repo forge.RepoRef, ref string) (string, string, string, error) {
	if err := f.before("ReadFence"); err != nil {
		return "", "", "", err
	}
	return f.Forge.ReadFence(ctx, repo, ref)
}

func (f *faultForge) ReadRef(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	if err := f.before("ReadRef"); err != nil {
		return "", err
	}
	return f.Forge.ReadRef(ctx, repo, ref)
}

func (f *faultForge) IsAncestor(ctx context.Context, repo forge.RepoRef, ancestor, descendant string) (bool, error) {
	if err := f.before("IsAncestor"); err != nil {
		return false, err
	}
	return f.Forge.IsAncestor(ctx, repo, ancestor, descendant)
}

func (f *faultForge) CommitAuthors(ctx context.Context, repo forge.RepoRef, number int) ([]forge.CommitAuthor, error) {
	if err := f.before("CommitAuthors"); err != nil {
		return nil, err
	}
	return f.Forge.CommitAuthors(ctx, repo, number)
}
func (f *faultForge) HeadBranchForDeletion(ctx context.Context, repo forge.RepoRef, number int, headSHA, base string) (string, error) {
	if err := f.before("HeadBranchForDeletion"); err != nil {
		return "", err
	}
	return f.Forge.HeadBranchForDeletion(ctx, repo, number, headSHA, base)
}
func (f *faultForge) DeleteBranch(ctx context.Context, repo forge.RepoRef, branch, expectedSHA string) error {
	defer f.sim.observeWrite()
	if err := f.before("DeleteBranch"); err != nil {
		return err
	}
	return f.Forge.DeleteBranch(ctx, repo, branch, expectedSHA)
}
