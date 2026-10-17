package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Real local HTTP check reads and rerun dispatch; all git/PR operations are fake.
type retryPermissionDriver struct {
	forge.Driver
	retry *Driver
}

func (d retryPermissionDriver) CheckRuns(ctx context.Context, repo forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	return d.retry.CheckRuns(ctx, repo, sha)
}
func (d retryPermissionDriver) CheckRunsForRetry(ctx context.Context, repo forge.RepoRef, sha string, workflows []uint64) ([]forge.CheckRun, error) {
	return d.retry.CheckRunsForRetry(ctx, repo, sha, workflows)
}
func (d retryPermissionDriver) RerunFailedChecks(ctx context.Context, repo forge.RepoRef, req forge.RerunChecksRequest) (forge.RerunChecksResult, error) {
	return d.retry.RerunFailedChecks(ctx, repo, req)
}

func TestExecutorActionsPermissionApprovalWindow(t *testing.T) {
	for _, outcome := range []string{"pass", "failure", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			a := newRerunAPI(t, 700, 800)
			a.ref = "openmerge/queue/main/b1"
			f := fake.New()
			base, fence := f.Init(testRepo, "main", "openmerge/fence/main")
			pr, err := f.OpenPR(testRepo, 1, base, true)
			must(t, err)
			now := epoch
			x := executor.Executor{Driver: retryPermissionDriver{Driver: f, retry: a.api.driver}, Git: f, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci-700", "ci-800"}, ChecksRetryFailed: 1, CheckTimeout: time.Minute, StartedTestingAt: epoch, Now: func() time.Time { return now }, NewID: func() (string, error) { return "land-attempt", nil }}
			cfg := queue.Config{MergeMethod: queue.Merge, ChecksRetryFailed: 1, CheckTimeout: time.Minute}
			s := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
			var events []queue.Event
			emit := func(e queue.Event) error { s = queue.Apply(s, e); events = append(events, e); return nil }
			must(t, emit(queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}))
			must(t, emit(queue.EnqueueRequested{PR: 1, At: epoch}))
			must(t, emit(queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, At: epoch}))
			build := queue.BuildTestRef{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, Ref: a.ref, Method: queue.Merge}
			must(t, x.Execute(t.Context(), build, emit))
			oldSHA := a.sha
			a.sha = s.Active.TestedSHA
			a.api.runs[a.sha] = a.api.runs[oldSHA]
			a.api.suites[a.sha] = a.api.suites[oldSHA]
			for _, run := range a.api.runs[a.sha] {
				run.HeadSHA = &a.sha
				if outcome == "pass" {
					run.Conclusion = new("success")
				}
				if outcome == "timeout" {
					run.Status = new("in_progress")
					run.Conclusion = nil
				}
			}
			if outcome == "pass" {
				// A recorded failed workflow keeps the passing landing read on the optional
				// enrichment path, proving a refused jobs read cannot block preflight.
				x.CheckRetry = &queue.CheckRetryState{FailedRuns: []queue.CheckRun{{WorkflowRunID: 700, ID: 1, Attempt: 1, Name: "ci-700", Source: "app:15368"}}}
			}
			old := a.api.override
			denied := 0
			a.api.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/actions/") {
					denied++
					a.api.apiError(w, 403, "Resource not accessible by integration")
					return true
				}
				return old(w, r)
			}
			must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b1", TestedSHA: a.sha}, emit))
			if !s.Active.CheckRetry.OriginalDeadline.Equal(epoch.Add(time.Minute)) {
				t.Fatal("lost timeout evidence")
			}
			if outcome == "timeout" {
				now = epoch.Add(61 * time.Second)
				must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b1", TestedSHA: a.sha}, emit))
			}
			for step := 0; step < 8; step++ {
				actions := queue.Plan(cfg, s, now)
				if len(actions) == 0 {
					break
				}
				var action queue.Action
				for _, next := range actions {
					switch next.(type) {
					case queue.Land, queue.RecordCheckRerun, queue.RerunChecks, queue.ConfirmCheckFailure:
						action = next
					}
					if action != nil {
						break
					}
				}
				if action == nil {
					break
				}
				x.CheckRetry = s.Active.CheckRetry
				// Keep enrichment active on passing preflight too.
				if outcome == "pass" {
					x.CheckRetry = &queue.CheckRetryState{FailedRuns: []queue.CheckRun{{WorkflowRunID: 700, ID: 1, Attempt: 1, Name: "ci-700", Source: "app:15368"}}}
				}
				must(t, x.Execute(t.Context(), action, emit))
			}
			switch outcome {
			case "pass":
				got, err := f.ReadRef(t.Context(), testRepo, "main")
				must(t, err)
				if got != a.sha {
					t.Fatalf("passing pre-approval batch did not land: %+v events=%+v", s, events)
				}
				if denied < 2 {
					t.Fatalf("did not exercise optional enrichment in refresh and preflight: %d", denied)
				}
			case "failure":
				if s.PendingDequeue(1) != queue.ChecksFailed || !s.Active.CheckRetry.Unsupported || !strings.Contains(s.Active.CheckRetry.Detail, "Actions permission not granted") {
					t.Fatalf("failure did not confirm unsupported: %+v", s.Active)
				}
			case "timeout":
				if s.PendingDequeue(1) != queue.ChecksTimeout {
					t.Fatalf("timeout did not qualify: %+v", s.Active)
				}
			}
			if a.posts[700] != 0 || a.posts[800] != 0 {
				t.Fatal("pre-approval failure sent POST")
			}
		})
	}
}
