package serve

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStageAExecuteApprovalWaitPrecedesCheckEvents(t *testing.T) {
	for _, retry := range []int{0, 1} {
		for _, actionName := range []string{"refresh", "land"} {
			t.Run(actionName+"/retry-"+string(rune('0'+retry)), func(t *testing.T) {
				s, w, f := shadowFixture(t, 1, "main", "7", false)
				f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue")))
				must(t, s.reconcile(t.Context(), w, true))
				w.cfg.Checks.RetryFailed = retry
				review, err := condition.Parse("approved-reviews-by = writer")
				must(t, err)
				mergeWhen := condition.All(w.cfg.MergeWhen, review)
				w.cfg.MergeWhen = mergeWhen

				cfg := w.plannerConfig(s.options.PollInterval)
				build := claimStageAAction(t, s, w, cfg, func(action queue.Action) bool {
					_, ok := action.(queue.BuildTestRef)
					return ok
				})
				must(t, s.execute(t.Context(), w, build))

				state := shadowState(t, s, w)
				batch := state.BatchForPR(1)
				if batch == nil || batch.TestedSHA == "" {
					t.Fatal("missing tested batch")
				}
				f.SetCheckRuns(repo, batch.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: batch.TestRef}})
				wake := []queue.Event{queue.RefreshRequested{}}
				if actionName == "land" {
					wake = append(wake, queue.ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true})
				}
				_, err = s.store.Append(t.Context(), w.id, wake, "")
				must(t, err)
				// Build's trust gate may reload the repository configuration. Keep
				// this regression's approval-only policy on the worker for the next
				// plan and for executor.CheckPolicyOutcome.
				w.cfg.Checks.RetryFailed = retry
				w.cfg.MergeWhen = mergeWhen
				cfg = w.plannerConfig(s.options.PollInterval)

				var wantAction func(queue.Action) bool
				if actionName == "refresh" {
					wantAction = func(action queue.Action) bool {
						_, ok := action.(queue.RefreshChecks)
						return ok
					}
				} else {
					wantAction = func(action queue.Action) bool {
						_, ok := action.(queue.Land)
						return ok
					}
				}
				action := claimStageAAction(t, s, w, cfg, wantAction)
				before, err := s.store.Watermark(t.Context(), w.id)
				must(t, err)
				err = s.execute(t.Context(), w, action)
				if actionName == "refresh" {
					must(t, err)
				} else if !errors.Is(err, executor.ErrLandingApprovals) {
					t.Fatalf("Land error = %v, want approval wait", err)
				}
				assertNoExecutorCheckEventsAfterApprovalWait(t, s, w, before)
			})
		}
	}
}

func claimStageAAction(t *testing.T, s *Server, w *worker, cfg queue.Config, want func(queue.Action) bool) store.ClaimedAction {
	t.Helper()
	_, err := s.store.Plan(t.Context(), w.id, cfg, time.Now())
	must(t, err)
	claims, err := s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, time.Now())
	must(t, err)
	for _, claim := range claims {
		if want(claim.Action) {
			return claim
		}
	}
	t.Fatal("requested Stage A action was not planned")
	return store.ClaimedAction{}
}

func assertNoExecutorCheckEventsAfterApprovalWait(t *testing.T, s *Server, w *worker, after int64) {
	t.Helper()
	records, err := s.store.EventsAfter(t.Context(), w.id, after)
	must(t, err)
	foundWait := false
	for _, record := range records {
		if _, ok := record.Event.(queue.ApprovalGateRejected); ok {
			foundWait = true
			continue
		}
		if !foundWait {
			continue
		}
		switch record.Event.(type) {
		case queue.CheckProvenanceObserved, queue.ChecksObserved, queue.ChecksCompleted, queue.ChecksReopened, queue.ChecksRefreshed:
			t.Fatalf("executor check event %T followed approval wait", record.Event)
		}
	}
	if !foundWait {
		t.Fatalf("approval wait was not recorded: %#v", records)
	}
}
