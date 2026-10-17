package github

import (
	"context"
	"errors"
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

func TestExecutorActionsPermissionLostDuringRerun(t *testing.T) {
	for _, outcome := range []string{"success", "pending", "failure", "granted-pass", "reconciliation", "preflight"} {
		t.Run(outcome, func(t *testing.T) {
			a := newRerunAPI(t, 700)
			a.ref = "openmerge/queue/main/b1"
			f := fake.New()
			base, fence := f.Init(testRepo, "main", "openmerge/fence/main")
			pr, err := f.OpenPR(testRepo, 1, base, true)
			must(t, err)
			now := epoch
			x := executor.Executor{Driver: retryPermissionDriver{Driver: f, retry: a.api.driver}, Git: f, Repo: testRepo, Base: "main", RequiredChecks: []string{"ci-700"}, ChecksRetryFailed: 1, CheckTimeout: time.Minute, StartedTestingAt: epoch, Now: func() time.Time { return now }, NewID: func() (string, error) { return "land-attempt", nil }}
			cfg := queue.Config{MergeMethod: queue.Merge, ChecksRetryFailed: 1, CheckTimeout: time.Minute}
			s := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
			var events []queue.Event
			emit := func(e queue.Event) error { s = queue.Apply(s, e); events = append(events, e); return nil }
			execute := func(action queue.Action) {
				t.Helper()
				x.CheckRetry = s.Active.CheckRetry
				must(t, x.Execute(t.Context(), action, emit))
			}
			must(t, emit(queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}))
			must(t, emit(queue.EnqueueRequested{PR: 1, At: epoch}))
			must(t, emit(queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, At: epoch}))
			execute(queue.BuildTestRef{BatchID: "b1", BaseSHA: base, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, Ref: a.ref, Method: queue.Merge})
			oldSHA := a.sha
			a.sha = s.Active.TestedSHA
			a.api.runs[a.sha] = a.api.runs[oldSHA]
			a.api.suites[a.sha] = a.api.suites[oldSHA]
			refresh := queue.RefreshChecks{BatchID: "b1", TestedSHA: a.sha}
			execute(refresh)
			var dispatch queue.RerunChecks
			for _, action := range queue.Plan(cfg, s, now) {
				if request, ok := action.(queue.RecordCheckRerun); ok {
					execute(request)
				}
			}
			for _, action := range queue.Plan(cfg, s, now) {
				if rerun, ok := action.(queue.RerunChecks); ok {
					dispatch = rerun
				}
			}
			x.CheckRetry = s.Active.CheckRetry
			crash := errors.New("acceptance persistence failed")
			err = x.Execute(t.Context(), dispatch, func(e queue.Event) error {
				if progress, ok := e.(queue.CheckRerunDispatchProgress); ok {
					if progress.Target.Accepted && outcome == "reconciliation" {
						return crash
					}
					a.journal = append(a.journal, forge.RerunTarget{WorkflowRunID: progress.Target.WorkflowRunID, Attempt: progress.Target.Attempt})
				}
				return emit(e)
			})
			if outcome == "reconciliation" {
				if !errors.Is(err, crash) {
					t.Fatalf("did not lose acceptance: %v", err)
				}
			} else {
				must(t, err)
			}
			if a.posts[700] != 1 {
				t.Fatalf("dispatch missing: %v", a.posts)
			}
			// Record the rerun's actual job attempt before permission is revoked.
			run := a.api.runs[a.sha][0]
			run.ID, run.Status, run.Conclusion = new(int64(702)), new("in_progress"), nil
			original := a.api.override
			denied := false
			a.api.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.Contains(r.URL.Path, "/actions/") {
					if denied {
						a.api.apiError(w, 403, "Resource not accessible by integration")
						return true
					}
					if strings.HasSuffix(r.URL.Path, "/jobs") {
						a.api.json(w, map[string]any{"jobs": []map[string]any{{"check_run_url": "https://github.test/api/v3/repos/acme/project/check-runs/702", "run_attempt": 2}}})
						return true
					}
				}
				return original(w, r)
			}
			if outcome != "reconciliation" {
				execute(refresh)
				if s.Active.CheckRetry.ObservedRuns[0].Attempt != 2 {
					t.Fatal("rerun attempt was not recorded")
				}
			}
			if outcome != "pending" {
				run.Status, run.Conclusion = new("completed"), new("success")
				if outcome == "failure" {
					run.Conclusion = new("failure")
				}
			}
			if outcome == "granted-pass" {
				execute(refresh)
				for _, action := range queue.Plan(cfg, s, now) {
					if land, ok := action.(queue.Land); ok {
						execute(land)
					}
				}
				got, err := f.ReadRef(t.Context(), testRepo, "main")
				must(t, err)
				if got != a.sha {
					t.Fatal("granted rerun pass did not land")
				}
				return
			}
			var land queue.Land
			if outcome == "preflight" {
				execute(refresh)
				for _, action := range queue.Plan(cfg, s, now) {
					if a, ok := action.(queue.Land); ok {
						land = a
					}
				}
			}
			denied = true
			now = epoch.Add(2 * time.Minute)
			before := len(events)
			switch outcome {
			case "reconciliation":
				execute(dispatch)
			case "preflight":
				x.CheckRetry = s.Active.CheckRetry
				if err := x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
					t.Fatalf("preflight=%v", err)
				}
			default:
				execute(refresh)
			}
			if len(events) != before+1 {
				t.Fatalf("permission refusal became check/timeout evidence: %+v", events[before:])
			}
			confirmed := false
			for _, action := range queue.Plan(cfg, s, now) {
				if confirm, ok := action.(queue.ConfirmCheckFailure); ok {
					if confirm.Reason != queue.ChecksFailed {
						t.Fatalf("false timeout: %+v", confirm)
					}
					execute(confirm)
					confirmed = true
				}
			}
			if !confirmed || s.Active.CheckRetry.Unsupported || s.PendingDequeue(1) != queue.ChecksFailed || s.CheckRetryDetail() != queue.ActionsPermissionLostDetail {
				t.Fatalf("unknown outcome not confirmed honestly: %+v", s.Active.CheckRetry)
			}
			for _, action := range queue.Plan(cfg, s, now) {
				if dequeue, ok := action.(queue.Dequeue); ok {
					x.ChecksTimeoutDetail = s.DequeueDetail(1, dequeue.Reason)
					execute(dequeue)
				}
			}
			if got := f.Statuses(testRepo, 1)[pr.HeadSHA]; !strings.Contains(got.Summary, queue.ActionsPermissionLostDetail) {
				t.Fatalf("PR status lost permission diagnostic: %+v", got)
			}
			got, err := f.ReadRef(t.Context(), testRepo, "main")
			must(t, err)
			if got != base || a.posts[700] != 1 {
				t.Fatalf("unknown outcome landed or redispatched: base=%s posts=%v", got, a.posts)
			}
		})
	}
}
