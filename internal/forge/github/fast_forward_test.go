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

func TestFastForwardHeadSuitesCannotAuthorizeMarker(t *testing.T) {
	for _, scenario := range []string{"same App", "other App", "historical same ref and SHA", "completed rerun", "fork null branch"} {
		t.Run(scenario, func(t *testing.T) {
			api := newAPI(t)
			git := fake.New()
			base, _ := git.Init(testRepo, "main", "openmerge/fence/main")
			pr, err := git.OpenPR(testRepo, 1, base, scenario != "fork null branch")
			must(t, err)
			fence, err := git.EnsureFence(t.Context(), testRepo, "openmerge/fence/main", forge.PushOptions{})
			must(t, err)
			cfg := queue.Config{MergeMethod: queue.FastForward}
			state := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
			state = queue.Apply(state, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
			state = queue.Apply(state, queue.EnqueueRequested{PR: 1, At: epoch})
			build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
			appID := api.app.id
			if scenario == "other App" {
				appID++
			}
			branch := new(build.Ref)
			if scenario == "fork null branch" {
				branch = nil
			}
			run := &gh.CheckRun{ID: new(int64(1000)), Name: new("ci"), App: &gh.App{ID: &appID}, Status: new("completed"), Conclusion: new("success"), HeadSHA: &pr.HeadSHA, CheckSuite: &gh.CheckSuite{ID: new(int64(99))}}
			api.runs[pr.HeadSHA] = []*gh.CheckRun{run}
			if scenario == "completed rerun" {
				old := *run
				old.ID = new(int64(999))
				api.runs[pr.HeadSHA] = append([]*gh.CheckRun{&old}, run)
			}
			api.suites[pr.HeadSHA] = []*gh.CheckSuite{{ID: new(int64(99)), Status: new("completed"), HeadBranch: branch}}
			x := executor.Executor{Driver: struct{ forge.Driver }{api.driver}, Git: git, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
			emit := func(e queue.Event) error { state = queue.Apply(state, e); return nil }
			must(t, x.Execute(t.Context(), build, emit))
			marker := state.Active.TestedSHA
			refresh := queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: marker}
			must(t, x.Execute(t.Context(), refresh, emit))
			land := queue.Land{BatchID: build.BatchID, ExpectedBaseSHA: base, TestedSHA: marker, FenceToken: fence, Ref: build.Ref, PRs: build.PRs, Method: queue.FastForward}
			if err := x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
				t.Fatalf("head suite authorized marker landing: %v", err)
			}
			if tip, err := git.ReadRef(t.Context(), testRepo, "main"); err != nil || tip != base {
				t.Fatal("head suite moved base")
			}
			markerRun := *run
			markerRun.ID, markerRun.HeadSHA = new(int64(1)), &marker
			api.runs[marker] = []*gh.CheckRun{&markerRun}
			api.suites[marker] = []*gh.CheckSuite{{ID: new(int64(99)), Status: new("completed"), HeadBranch: &build.Ref}}
			must(t, x.Execute(t.Context(), refresh, emit))
			must(t, x.Execute(t.Context(), land, emit))
			if tip, err := git.ReadRef(t.Context(), testRepo, "main"); err != nil || tip != pr.HeadSHA {
				t.Fatal("marker suite did not land original head")
			}
		})
	}
}
