package github

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	gh "github.com/google/go-github/v93/github"
)

func TestReviewR3HistoricalMarkerAndOtherBranch(t *testing.T) {
	api := newAPI(t)
	git := fake.New()
	base, fence := git.Init(testRepo, "main", "openmerge/fence/main")
	pr, err := git.OpenPR(testRepo, 1, base, true)
	must(t, err)
	cfg := queue.Config{MergeMethod: queue.FastForward}
	state := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
	state = queue.Apply(state, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
	state = queue.Apply(state, queue.EnqueueRequested{PR: 1, At: epoch})
	build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
	older, err := git.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: build.Ref, BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: pr.HeadSHA}}, BatchID: build.BatchID, Method: forge.FastForward}, forge.PushOptions{})
	must(t, err)
	attach := func(sha, branch string) {
		api.runs[sha] = []*gh.CheckRun{{ID: new(int64(999)), Name: new("ci"), App: &gh.App{ID: new(int64(15368))}, Status: new("completed"), Conclusion: new("success"), HeadSHA: &sha, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}}}
		api.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &branch}}
	}
	attach(pr.HeadSHA, build.Ref)
	attach(older, build.Ref)
	x := executor.Executor{Driver: struct{ forge.Driver }{api.driver}, Git: git, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
	emit := func(e queue.Event) error { state = queue.Apply(state, e); return nil }
	must(t, x.Execute(t.Context(), build, emit))
	marker := state.Active.TestedSHA
	if marker == older || marker == pr.HeadSHA {
		t.Fatal("reused earlier identity")
	}
	must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: marker}, emit))
	land := queue.Land{BatchID: build.BatchID, ExpectedBaseSHA: base, TestedSHA: marker, FenceToken: fence, Ref: build.Ref, PRs: build.PRs, Method: queue.FastForward}
	if err := x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
		t.Fatalf("historical suite permitted landing: %v", err)
	}
	// Model a queued push job, then a newer completed pull_request job on a
	// contributor copy of this now-public marker. Both have the Actions App ID.
	must(t, git.SetRef(testRepo, "contributor/reposted-marker", marker))
	attach(marker, "contributor/reposted-marker")
	api.runs[marker] = append([]*gh.CheckRun{{ID: new(int64(100)), Name: new("ci"), App: &gh.App{ID: new(int64(15368))}, Status: new("in_progress"), HeadSHA: &marker, CheckSuite: &gh.CheckSuite{ID: new(int64(10))}}}, api.runs[marker]...)
	api.suites[marker] = append([]*gh.CheckSuite{{ID: new(int64(10)), Status: new("in_progress"), HeadBranch: &build.Ref}}, api.suites[marker]...)
	err = x.Execute(t.Context(), land, emit)
	if errors.Is(err, executor.ErrChecksNotSuccessful) {
		return
	}
	must(t, err)
	tip, err := git.ReadRef(t.Context(), testRepo, "main")
	must(t, err)
	if tip != pr.HeadSHA {
		t.Fatal("expected marker-SHA checks to land head")
	}
	t.Fatal("non-queue completed run superseded pending queue CI and landed the pinned head")
}
