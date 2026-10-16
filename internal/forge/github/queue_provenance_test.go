package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	gh "github.com/google/go-github/v93/github"
)

func TestQueueCheckProvenanceForEveryMergeMethod(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
		for _, branch := range []*string{new("contributor/reposted-marker"), nil, new("openmerge/queue/main/old-batch"), new("refs/heads/openmerge/queue/main/b1")} {
			name := "null"
			if branch != nil {
				name = *branch
			}
			t.Run(string(method)+"/"+name, func(t *testing.T) {
				api, git := newAPI(t), fake.New()
				base, fence := git.Init(testRepo, "main", "openmerge/fence/main")
				pr, err := git.OpenPR(testRepo, 1, base, true)
				must(t, err)
				cfg := queue.Config{MergeMethod: method}
				state := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
				state = queue.Apply(state, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
				state = queue.Apply(state, queue.EnqueueRequested{PR: 1, At: epoch})
				build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
				x := executor.Executor{Driver: struct{ forge.Driver }{api.driver}, Git: git, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
				var events []queue.Event
				emit := func(e queue.Event) error { events = append(events, e); state = queue.Apply(state, e); return nil }
				must(t, x.Execute(t.Context(), build, emit))
				tested := state.Active.TestedSHA
				// A contributor copies the published test commit and gets a newer
				// successful pull_request run from the same Actions App.
				must(t, git.SetRef(testRepo, "contributor/reposted-marker", tested))
				queueRun := &gh.CheckRun{ID: new(int64(100)), Name: new("ci"), App: &gh.App{ID: new(int64(15368))}, Status: new("in_progress"), HeadSHA: &tested, CheckSuite: &gh.CheckSuite{ID: new(int64(10))}}
				foreign := &gh.CheckRun{ID: new(int64(999)), Name: new("ci"), App: &gh.App{ID: new(int64(15368))}, Status: new("completed"), Conclusion: new("success"), HeadSHA: &tested, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}}
				api.runs[tested] = []*gh.CheckRun{foreign, queueRun}
				api.suites[tested] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: branch}, {ID: new(int64(10)), Status: new("in_progress"), HeadBranch: &build.Ref}}
				refresh := queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: tested}
				land := queue.Land{BatchID: build.BatchID, ExpectedBaseSHA: base, TestedSHA: tested, FenceToken: fence, Ref: build.Ref, PRs: build.PRs, Method: method}
				assertPending := func() {
					t.Helper()
					events = nil
					must(t, x.Execute(t.Context(), refresh, emit))
					if err := x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
						t.Fatalf("foreign suite authorized landing: %v", err)
					}
					for _, e := range events {
						switch e.(type) {
						case queue.ChecksCompleted, queue.LandingStarted:
							t.Fatalf("foreign suite produced terminal event: %#v", e)
						}
					}
					for _, ref := range []string{"main", "openmerge/fence/main"} {
						tip, err := git.ReadRef(t.Context(), testRepo, ref)
						must(t, err)
						if ref == "main" && tip != base || ref != "main" && tip != fence {
							t.Fatal("pending queue checks moved base or fence")
						}
					}
				}
				assertPending()
				// Unknown provenance alone is ignored, including a foreign failure.
				foreign.Conclusion = new("failure")
				api.runs[tested] = []*gh.CheckRun{foreign}
				assertPending()
				// A pending rerun of the matching suite supersedes an older pass.
				queueRun.Status, queueRun.Conclusion = new("completed"), new("success")
				api.suites[tested][1].Status = new("completed")
				rerun := *queueRun
				rerun.ID, rerun.Status = new(int64(101)), new("in_progress")
				api.runs[tested] = []*gh.CheckRun{foreign, &rerun, queueRun}
				assertPending()
				// Foreign producers must also be removed before source ambiguity.
				foreign.App.ID = new(int64(222))
				rerun.Status = new("completed")
				must(t, x.Execute(t.Context(), refresh, emit))
				if !reviewCompletedSuccess(events) {
					t.Fatal("matching suite rerun did not pass")
				}
				must(t, x.Execute(t.Context(), land, emit))
				want := tested
				if method == queue.FastForward {
					want = pr.HeadSHA
				}
				tip, err := git.ReadRef(t.Context(), testRepo, "main")
				must(t, err)
				if tip != want {
					t.Fatal("matching queue CI did not land the expected commit")
				}
			})
		}
	}
}

func TestPushCheckSuiteFixture(t *testing.T) {
	body := webhookFixture(t, "check_suite_push")
	event, err := VerifyAndParse(signedRequest("check_suite", body), webhookSecret)
	must(t, err)
	sha := event.Payload.(ChecksChanged).SHA
	var payload struct {
		Suite json.RawMessage `json:"check_suite"`
	}
	must(t, json.Unmarshal(body, &payload))
	api := newAPI(t)
	api.runs[sha] = []*gh.CheckRun{{ID: new(int64(100)), Name: new("check"), HeadSHA: &sha, App: &gh.App{ID: new(int64(15368))}, Status: new("completed"), Conclusion: new("success"), CheckSuite: &gh.CheckSuite{ID: new(int64(99))}}}
	api.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/check-suites") {
			api.json(w, map[string]any{"total_count": 1, "check_suites": []json.RawMessage{payload.Suite}})
			return true
		}
		return false
	}
	runs, err := api.driver.CheckRuns(t.Context(), testRepo, sha)
	must(t, err)
	if len(runs) != 1 || runs[0].HeadBranch != "openmerge/queue/main/b7" || runs[0].State != forge.Success {
		t.Fatalf("push suite provenance lost: %+v", runs)
	}
	x := executor.Executor{Driver: api.driver, Repo: testRepo, Base: "main", RequiredChecks: []string{"check"}}
	var events []queue.Event
	must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b7", TestedSHA: sha}, func(e queue.Event) error { events = append(events, e); return nil }))
	if !reviewCompletedSuccess(events) {
		t.Fatalf("queue push suite did not satisfy CI: %+v", events)
	}
}
