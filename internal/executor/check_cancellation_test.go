package executor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type cancellationDriver struct {
	forge.Driver
	cancel func(context.Context, forge.RepoRef, forge.CancelChecksRequest) (forge.CancelChecksResult, error)
}

func (d cancellationDriver) CancelChecks(ctx context.Context, r forge.RepoRef, a forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	return d.cancel(ctx, r, a)
}

func TestCancellationInFlightWhileSuccessorBecomesLandingHead(t *testing.T) {
	c := newChainExecutor(t, 3)
	first, second, retired := c.state.Batches()[0], c.state.Batches()[1], c.state.Batches()[2]
	for i, b := range c.state.Batches() {
		state := forge.Success
		if i == 2 {
			state = forge.Pending
		}
		c.checks(b, uint64(i+1), 1, state)
		c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, b.ID))
	}
	c.state = queue.Apply(c.state, queue.PRClosed{PR: 3})
	c.execute(t, plannedChainAction[queue.InvalidateBatches](t, c, ""))
	c.execute(t, plannedChainAction[queue.RecordCheckCancellation](t, c, ""))
	cancel := plannedChainAction[queue.CancelChecks](t, c, "")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var events []queue.Event
	x := *c.x
	x.Driver = cancellationDriver{Driver: c.f, cancel: func(ctx context.Context, r forge.RepoRef, a forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
		if a.TestedSHA != retired.TestedSHA || a.TestRef != retired.TestRef || len(a.Runs) != 1 || a.Runs[0].ID != 3 {
			return forge.CancelChecksResult{}, errors.New("wrong cancellation target")
		}
		close(entered)
		<-release
		return c.f.CancelChecks(ctx, r, a)
	}}
	go func() { done <- x.Execute(t.Context(), cancel, record(&events)) }()
	<-entered
	c.execute(t, plannedChainAction[queue.Land](t, c, first.ID))
	if c.state.Active.ID != second.ID {
		t.Fatal("successor did not become head")
	}
	c.execute(t, plannedChainAction[queue.Land](t, c, second.ID))
	close(release)
	must(t, <-done)
	for _, event := range events {
		c.state = queue.Apply(c.state, event)
	}
	if c.read(t, "main") != second.LandingCommit() {
		t.Fatal("live successor did not land")
	}
	for _, b := range []*queue.Batch{first, second} {
		runs, err := c.f.CheckRuns(t.Context(), repo, b.TestedSHA)
		must(t, err)
		if runs[0].State != forge.Success {
			t.Fatal("cancellation affected landing/landed run")
		}
	}
	// A replay after the result was lost converges on the same retired identity.
	must(t, c.x.Execute(t.Context(), cancel, func(queue.Event) error { return nil }))
}
func TestCancellationRequestMustCommitBeforeDispatch(t *testing.T) {
	h := setup(t)
	called := false
	h.x.Driver = cancellationDriver{Driver: h.f, cancel: func(context.Context, forge.RepoRef, forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
		called = true
		return forge.CancelChecksResult{}, nil
	}}
	a := queue.RecordCheckCancellation{BatchID: "old", TestedSHA: "sha", TestRef: "ref", Reason: "closed", Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}}
	if err := h.x.Execute(t.Context(), a, func(queue.Event) error { return errLog }); !errors.Is(err, errLog) || called {
		t.Fatal("dispatch preceded durable intent")
	}
}
func TestCancellationRequestedConfirmedUnsupportedAndRestart(t *testing.T) {
	for _, mode := range []string{"pending", "confirmed", "unsupported", "missing"} {
		t.Run(mode, func(t *testing.T) {
			h := setup(t)
			calls := 0
			a := queue.CancelChecks{BatchID: "old", TestedSHA: "sha", TestRef: "ref", Reason: "closed", Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}}
			if mode == "missing" {
				h.x.Driver = struct{ forge.Driver }{h.f}
			} else {
				h.x.Driver = cancellationDriver{Driver: h.f, cancel: func(_ context.Context, _ forge.RepoRef, r forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
					calls++
					if r.Key != a.Key() || r.Runs[0].HeadBranch != a.TestRef {
						t.Fatal("target not pinned")
					}
					switch mode {
					case "confirmed":
						r.Runs[0].State = forge.Cancelled
						return forge.CancelChecksResult{Confirmed: r.Runs}, nil
					case "unsupported":
						return forge.CancelChecksResult{Unsupported: "permission not granted"}, nil
					default:
						return forge.CancelChecksResult{}, nil
					}
				}}
			}
			for range 2 {
				var events []queue.Event
				err := h.x.Execute(t.Context(), a, record(&events))
				if (mode == "pending") != errors.Is(err, executor.ErrCancellationPending) {
					t.Fatalf("pending result=%v", err)
				}
				result, ok := events[0].(queue.CheckCancellationReported)
				if !ok {
					t.Fatal("outcome missing")
				}
				if result.Unsupported != (mode == "unsupported" || mode == "missing") || (len(result.Confirmed) > 0) != (mode == "confirmed") {
					t.Fatalf("wrong outcome:%+v", result)
				}
			}
			if mode != "missing" && calls != 2 {
				t.Fatal("restart did not repeat idempotent request")
			}
		})
	}
}

func TestCancellationUnsupportedFollowupObservesOnlyRecordedRuns(t *testing.T) {
	h := setup(t)
	a := queue.ObserveRetiredChecks{BatchID: "old", TestedSHA: "sha", TestRef: "openmerge/queue/main/old", Reason: "closed", Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}}
	for _, mode := range []string{"pending", "wrong-ref", "wrong-attempt", "success"} {
		t.Run(mode, func(t *testing.T) {
			run := forge.CheckRun{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", HeadBranch: a.TestRef, State: forge.Success}
			switch mode {
			case "pending":
				run.State = forge.Pending
			case "wrong-ref":
				run.HeadBranch = "main"
			case "wrong-attempt":
				run.Attempt = 2
			}
			h.f.SetCheckRuns(repo, a.TestedSHA, []forge.CheckRun{run})
			var events []queue.Event
			err := h.x.Execute(t.Context(), a, record(&events))
			if mode != "success" {
				if !errors.Is(err, executor.ErrCancellationPending) {
					t.Fatalf("unconfirmed observation settled: %v", err)
				}
			} else {
				must(t, err)
			}
			for _, event := range events {
				switch e := event.(type) {
				case queue.ChecksObserved:
					if mode == "wrong-ref" || mode == "wrong-attempt" {
						t.Fatal("unrecorded identity observed")
					}
					if e.BatchID != a.BatchID || e.TestedSHA != a.TestedSHA || e.Runs[0].ID != 1 {
						t.Fatal("observation broadened")
					}
				case queue.ActionCompleted:
					if mode != "success" {
						t.Fatal("pending history completed")
					}
				}
			}
		})
	}
}

type retiredAttemptReader struct {
	forge.Driver
	called bool
}

func (r *retiredAttemptReader) CheckRunsForRetry(_ context.Context, _ forge.RepoRef, sha string, workflows []uint64) ([]forge.CheckRun, error) {
	r.called = sha == "sha" && len(workflows) == 1 && workflows[0] == 700
	return []forge.CheckRun{{ID: 702, Attempt: 2, WorkflowRunID: 700, Name: "ci", Source: "app:15368", HeadBranch: "openmerge/queue/main/old", State: forge.Success}}, nil
}
func TestCancellationRetiredRerunUsesAttemptAwareReader(t *testing.T) {
	h := setup(t)
	reader := &retiredAttemptReader{Driver: h.f}
	h.x.Driver = reader
	a := queue.ObserveRetiredChecks{BatchID: "old", TestedSHA: "sha", TestRef: "openmerge/queue/main/old", Reason: "closed", Runs: []queue.CheckRun{{ID: 702, Attempt: 2, WorkflowRunID: 700, Name: "ci", Source: "app:15368", State: "pending"}}}
	var events []queue.Event
	must(t, h.x.Execute(t.Context(), a, record(&events)))
	if !reader.called || len(events) != 2 {
		t.Fatal("retired rerun completion not enriched")
	}
	if observed, ok := events[0].(queue.ChecksObserved); !ok || observed.Runs[0].Attempt != 2 || observed.Runs[0].State != "success" {
		t.Fatalf("wrong completion: %+v", events)
	}
}

func TestCancellationHistorySuppressesUnchangedAndEmptyRuns(t *testing.T) {
	c := newChainExecutor(t, 1)
	b := c.state.Active
	c.checks(b, 101, 1, forge.Pending)
	refresh := plannedChainAction[queue.RefreshChecks](t, c, b.ID)
	c.execute(t, refresh)
	c.x.PreviousObservedRuns = c.state.Active.LastObservedRuns
	for _, mode := range []string{"unchanged", "empty"} {
		if mode == "empty" {
			c.f.SetCheckRuns(repo, b.TestedSHA, nil)
		}
		var events []queue.Event
		must(t, c.x.Execute(t.Context(), refresh, record(&events)))
		for _, event := range events {
			if _, ok := event.(queue.BatchRunsObserved); ok {
				t.Fatalf("%s history emitted", mode)
			}
		}
	}
}

func TestCancellationDepthOneRetryCycleUsesOnlyExistingProtocol(t *testing.T) {
	h := setup(t)
	c := &chainExecutor{harness: h, cfg: queue.Config{MergeMethod: queue.Merge, ChecksRetryFailed: 1, CheckTimeout: time.Minute}, now: epoch}
	c.x.ChecksRetryFailed = 1
	c.x.CheckTimeout = c.cfg.CheckTimeout
	c.x.RequiredChecks = []string{"ci"}
	c.state = queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
	c.state = queue.Apply(c.state, queue.PRObserved{PR: 1, HeadSHA: h.prs[0].HeadSHA, Eligible: true, Trusted: true})
	c.state = queue.Apply(c.state, queue.EnqueueRequested{PR: 1, At: epoch})
	var events []queue.Event
	emit := func(e queue.Event) error {
		events = append(events, e)
		switch e.(type) {
		case queue.BatchRunsObserved, queue.CheckCancellationRequested, queue.CheckCancellationReported, queue.CheckCancellationDeferred:
			t.Fatalf("depth-one emitted PR 4 event %T", e)
		}
		c.state = queue.Apply(c.state, e)
		return nil
	}
	execute := func(a queue.Action) {
		t.Helper()
		if b := c.state.ActionBatch(a); b != nil {
			c.x.StartedTestingAt = b.StartedTestingAt
			c.x.CheckRetry = b.CheckRetry
		}
		must(t, c.x.Execute(t.Context(), a, emit))
	}
	execute(plannedChainAction[queue.BuildTestRef](t, c, ""))
	b := c.state.Active
	c.checks(b, 1, 1, forge.Failure)
	execute(plannedChainAction[queue.RefreshChecks](t, c, b.ID))
	execute(plannedChainAction[queue.RecordCheckRerun](t, c, b.ID))
	execute(plannedChainAction[queue.RerunChecks](t, c, b.ID))
	c.checks(b, 2, 2, forge.Success)
	c.state = queue.Apply(c.state, queue.RefreshRequested{})
	execute(plannedChainAction[queue.RefreshChecks](t, c, b.ID))
	land := plannedChainAction[queue.Land](t, c, b.ID)
	c.checks(b, 2, 2, forge.Failure)
	if err := c.x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
		t.Fatalf("failed land preflight=%v", err)
	}
	c.state = queue.Apply(c.state, queue.PRClosed{PR: 1})
	for _, a := range queue.Plan(c.cfg, c.state, c.now) {
		switch a.(type) {
		case queue.RecordCheckCancellation, queue.CancelChecks, queue.ObserveRetiredChecks, queue.RetryCheckCancellation:
			t.Fatalf("depth-one emitted PR 4 action %T", a)
		}
	}
	if len(events) == 0 {
		t.Fatal("empty cycle")
	}
}
