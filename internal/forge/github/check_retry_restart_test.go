package github

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestActionsRerunRestartReplaysDurableIntent(t *testing.T) {
	url := serveDatabaseURL(t)
	db, err := store.Open(t.Context(), url)
	must(t, err)
	a := newRerunAPI(t, 700)
	a.ref = "openmerge/queue/main/b1"
	q, err := db.EnsureQueue(t.Context(), store.QueueKey{Installation: "7", Repository: testRepo.Path, BaseBranch: "main"})
	must(t, err)
	runs, err := a.api.driver.CheckRuns(t.Context(), testRepo, a.sha)
	must(t, err)
	run := runs[0]
	failed := []queue.CheckRun{{ID: run.ID, Attempt: run.Attempt, Name: run.Name, Source: run.Source, State: "failure", WorkflowRunID: run.WorkflowRunID, WorkflowRunURL: run.WorkflowRunURL, URL: run.URL}}
	cfg := queue.Config{ChecksRetryFailed: 1, CheckTimeout: time.Minute}
	_, err = db.Append(t.Context(), q, []queue.Event{
		queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: a.sha, Method: queue.Merge, At: epoch},
		queue.ChecksObserved{BatchID: "b1", TestedSHA: a.sha, Runs: failed, Terminal: true, OriginalDeadline: epoch.Add(time.Minute), Timeout: time.Minute},
	}, "")
	must(t, err)
	now := epoch
	x := executor.Executor{Driver: a.api.driver, Repo: testRepo, Base: "main", RequiredChecks: []string{run.Name}, ChecksRetryFailed: 1, CheckTimeout: time.Minute, StartedTestingAt: epoch, Now: func() time.Time { return now }}
	claim := func() store.ClaimedAction {
		_, err := db.Plan(t.Context(), q, cfg, now)
		must(t, err)
		claims, err := db.ClaimQueue(t.Context(), q, 1, 10*time.Second, now)
		must(t, err)
		if len(claims) != 1 {
			t.Fatalf("claims=%+v", claims)
		}
		proceed, err := db.BeginConsequential(t.Context(), claims[0], cfg, now)
		must(t, err)
		if !proceed {
			t.Fatal("current action declined")
		}
		return claims[0]
	}
	request := claim()
	must(t, x.Execute(t.Context(), request.Action, db.Emit(t.Context(), request, cfg)))
	must(t, db.Complete(t.Context(), request, store.Done))
	dispatch := claim()
	state, _, err := db.Load(t.Context(), q)
	must(t, err)
	x.CheckRetry = state.Active.CheckRetry
	crash := errors.New("crash after remote acceptance, before acceptance event")
	err = x.Execute(t.Context(), dispatch.Action, func(event queue.Event) error {
		progress, ok := event.(queue.CheckRerunDispatchProgress)
		if ok && progress.Target.Accepted {
			return crash
		}
		err := db.Emit(t.Context(), dispatch, cfg)(event)
		if err == nil && ok {
			a.journal = append(a.journal, forge.RerunTarget{WorkflowRunID: progress.Target.WorkflowRunID, Attempt: progress.Target.Attempt})
		}
		return err
	})
	if !errors.Is(err, crash) || a.posts[700] != 1 {
		t.Fatalf("first execution=%v posts=%v", err, a.posts)
	}
	db.Close()
	a.restart()
	a.journal = nil
	x.Driver = a.api.driver
	db, err = store.Open(t.Context(), url)
	must(t, err)
	defer db.Close()
	// Lease expires while this process is absent; the replacement reloads events.
	now = epoch.Add(11 * time.Second)
	replacement := claim()
	state, _, err = db.Load(t.Context(), q)
	must(t, err)
	if len(state.Active.CheckRetry.Dispatches) != 1 || state.Active.CheckRetry.Dispatches[0].Accepted {
		t.Fatalf("lost durable intent: %+v", state.Active.CheckRetry)
	}
	x.CheckRetry = state.Active.CheckRetry
	err = x.Execute(t.Context(), replacement.Action, db.Emit(t.Context(), replacement, cfg))
	if err == nil || a.posts[700] != 1 {
		t.Fatalf("unchanged failure authorized redispatch: %v posts=%v", err, a.posts)
	}
	// GitHub eventually exposes the accepted attempt, while the original job
	// remains in check-runs. Reconciliation completes without another mutation.
	a.workflows[700].RunAttempt = new(2)
	must(t, x.Execute(t.Context(), replacement.Action, db.Emit(t.Context(), replacement, cfg)))
	state, _, err = db.Load(t.Context(), q)
	must(t, err)
	retry := state.Active.CheckRetry
	if !retry.Dispatched || !retry.Dispatches[0].Accepted || a.posts[700] != 1 || retry.FailedRuns[0].URL != run.URL {
		t.Fatalf("reconciled retry=%+v posts=%v", retry, a.posts)
	}
}
