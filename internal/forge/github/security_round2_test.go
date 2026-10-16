package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	gh "github.com/google/go-github/v93/github"
)

func passingCI(f *apiFixture) {
	f.runs["tested"] = []*gh.CheckRun{{ID: new(int64(100)), Name: new("CI"), Status: new("completed"), Conclusion: new("success"), CheckSuite: &gh.CheckSuite{ID: new(int64(9))}, App: &gh.App{ID: new(int64(11))}}}
	f.suites["tested"] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadSHA: new("tested"), HeadBranch: new("openmerge/queue/main/batch"), App: &gh.App{ID: new(int64(11))}}}
}

func TestMismatched304CannotReplayPermissionOrChecks(t *testing.T) {
	for _, mode := range []string{"permission", "checks"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			passingCI(f)
			if mode == "permission" {
				allowed, err := f.driver.CanWrite(context.Background(), testRepo, "writer")
				must(t, err)
				if !allowed {
					t.Fatal("fixture denied")
				}
			} else {
				events, err := reviewRefresh(f)
				must(t, err)
				if !reviewCompletedSuccess(events) {
					t.Fatal("fixture failed")
				}
			}
			f.permissions["writer"] = "read"
			f.runs["tested"][0].Conclusion = new("failure")
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if (mode == "permission" && strings.HasSuffix(r.URL.Path, "/permission")) || (mode == "checks" && strings.HasSuffix(r.URL.Path, "/check-runs")) {
					if r.Header.Get("If-None-Match") == "" {
						t.Error("probe did not revalidate")
					}
					w.Header().Set("ETag", `"different-representation"`)
					w.WriteHeader(304)
					return true
				}
				return false
			}
			if mode == "permission" {
				allowed, err := f.driver.CanWrite(context.Background(), testRepo, "writer")
				if err == nil && allowed {
					t.Fatal("REPRO: mismatched 304 replays revoked write grant")
				}
			} else {
				events, err := reviewRefresh(f)
				if err == nil && reviewCompletedSuccess(events) {
					t.Fatal("REPRO: mismatched 304 replays old passing CI")
				}
			}
		})
	}
}

func TestPendingRefreshRevokesRecordedSuccess(t *testing.T) {
	for _, mode := range []string{"rerequested-suite", "new-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			passingCI(f)
			cfg := queue.Config{MergeMethod: queue.Merge, CheckTimeout: time.Minute}
			s := queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
			s = queue.Apply(s, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true})
			s = queue.Apply(s, queue.EnqueueRequested{PR: 1, At: epoch})
			var build queue.BuildTestRef
			for _, a := range queue.Plan(cfg, s, epoch) {
				if b, ok := a.(queue.BuildTestRef); ok {
					build = b
				}
			}
			if build.BatchID == "" {
				t.Fatal("no build")
			}
			s = queue.Apply(s, queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: epoch})
			s = queue.Apply(s, queue.TestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested", Method: queue.Merge, At: epoch})
			f.suites["tested"][0].HeadBranch = &build.Ref
			x := executor.Executor{Driver: f.driver, Repo: testRepo, Base: "main", RequiredChecks: []string{"CI"}}
			emit := func(e queue.Event) error { s = queue.Apply(s, e); return nil }
			a := queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: "tested"}
			must(t, x.Execute(context.Background(), a, emit))
			if mode == "rerequested-suite" {
				f.suites["tested"][0].Status = new("queued")
			} else {
				f.runs["tested"] = append(f.runs["tested"], &gh.CheckRun{ID: new(int64(101)), Name: new("CI"), Status: new("completed"), Conclusion: new("success"), CheckSuite: &gh.CheckSuite{ID: new(int64(10))}, App: &gh.App{ID: new(int64(22))}})
				f.suites["tested"] = append(f.suites["tested"], &gh.CheckSuite{ID: new(int64(10)), Status: new("completed"), HeadBranch: &build.Ref})
			}
			err := x.Execute(context.Background(), a, emit)
			if mode == "rerequested-suite" {
				must(t, err)
			} else {
				var ambiguity *executor.CheckSourceAmbiguityError
				if !errors.As(err, &ambiguity) {
					t.Fatalf("missing diagnostic: %v", err)
				}
			}
			for _, a := range queue.Plan(cfg, s, epoch) {
				if _, ok := a.(queue.Land); ok {
					t.Fatalf("REPRO: %s was freshly observed but queue still plans Land", mode)
				}
			}
		})
	}
}
