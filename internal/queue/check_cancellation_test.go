package queue

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func observeCancellationRun(s State, b *Batch) State {
	return Apply(s, ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Runs: []CheckRun{{ID: b.ChainPosition, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}, OriginalDeadline: b.StartedTestingAt.Add(time.Hour), Timeout: time.Hour})
}
func TestCancellationRetiresBeforeRequestAndSurvivesReplay(t *testing.T) {
	for _, cause := range []string{"head", "dequeue", "closed", "base", "config", "failure"} {
		t.Run(cause, func(t *testing.T) {
			cfg, s, at := chainState(t, 4, 3)
			s, first := admitChain(t, cfg, s, at)
			s, second := admitChain(t, cfg, s, at)
			s = observeCancellationRun(s, first)
			s = observeCancellationRun(s, second)
			switch cause {
			case "head":
				s = Apply(s, PRObserved{PR: 1, HeadSHA: "new", Trusted: true, Eligible: true})
			case "dequeue":
				s = Apply(s, DequeueRequested{PR: 1})
			case "closed":
				s = Apply(s, PRClosed{PR: 1})
			case "base":
				s = Apply(s, BaseMoved{SHA: "outside"})
			case "config":
				s = Apply(s, ConfigApplied{Commit: "config", Digest: strings.Repeat("a", 64)})
				cfg.ConfigDigest = strings.Repeat("a", 64)
			case "failure":
				s = Apply(s, ChecksFailureConfirmed{BatchID: first.ID, TestedSHA: first.TestedSHA, Generation: s.Active.checksGeneration, Reason: ChecksTimeout})
			}
			for _, action := range Plan(cfg, s, at) {
				if _, ok := action.(CancelChecks); ok {
					t.Fatal("cancel before retirement")
				}
			}
			invalidation := onlyAction[InvalidateBatches](t, Plan(cfg, s, at))
			s = Apply(s, BatchesInvalidated(invalidation))
			var request RecordCheckCancellation
			for _, a := range Plan(cfg, s, at) {
				if a, ok := a.(RecordCheckCancellation); ok && a.BatchID == second.ID {
					request = a
				}
			}
			if request.BatchID == "" || request.TestedSHA != second.TestedSHA || request.TestRef != second.TestRef {
				t.Fatal("lost exact targets")
			}
			s = snapshotRoundTrip(t, cfg, s, at)
			s = Apply(s, CheckCancellationRequested(request))
			s = Apply(s, CheckCancellationRequested(request))
			s = snapshotRoundTrip(t, cfg, s, at)
			if s.Batch(second.ID) != nil {
				t.Fatal("retired batch revived")
			}
			count := 0
			for _, a := range Plan(cfg, s, at) {
				if a, ok := a.(CancelChecks); ok && a.BatchID == second.ID {
					count++
					if !slices.Equal(a.Runs, request.Runs) {
						t.Fatal("target changed")
					}
				}
			}
			if count != 1 {
				t.Fatalf("pending cancellations=%d", count)
			}
			// Delayed terminal success and a durable start cannot revive a retired ID.
			s = Apply(s, ChecksCompleted{TestedSHA: second.TestedSHA, Success: true})
			s = Apply(s, LandingStarted{BatchID: second.ID, AttemptID: "late", FenceToken: s.Fence, At: at})
			if s.Batch(second.ID) != nil {
				t.Fatal("late result revived retired batch")
			}
			confirmed := slices.Clone(request.Runs)
			confirmed[0].State = "cancelled"
			s = Apply(s, CheckCancellationReported{BatchID: request.BatchID, TestedSHA: request.TestedSHA, Confirmed: confirmed})
			for _, a := range Plan(cfg, s, at) {
				if a, ok := a.(CancelChecks); ok && a.BatchID == second.ID {
					t.Fatal("confirmed cancellation not settled")
				}
			}
		})
	}
}
func TestCancellationNeverTargetsLandingOrLanded(t *testing.T) {
	cfg, s, at := chainState(t, 4, 2)
	s, first := admitChain(t, cfg, s, at)
	s, second := admitChain(t, cfg, s, at)
	s = observeCancellationRun(s, first)
	s = observeCancellationRun(s, second)
	s = Apply(s, ChecksCompleted{TestedSHA: first.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: first.ID, AttemptID: "landing", FenceToken: s.Fence, At: at})
	s = Apply(s, PRClosed{PR: 1})
	s = Apply(s, BatchesInvalidated{BatchIDs: []string{first.ID}, Reason: "change-left-queue"})
	forged := CheckCancellationRequested{BatchID: first.ID, TestedSHA: first.TestedSHA, TestRef: first.TestRef, Runs: s.Batch(first.ID).CheckRetry.ObservedRuns, Reason: "closed"}
	s = Apply(s, forged)
	for _, a := range Plan(cfg, s, at) {
		switch a := a.(type) {
		case RecordCheckCancellation:
			if a.BatchID == first.ID {
				t.Fatal("landing cancelled")
			}
		case CancelChecks:
			if a.BatchID == first.ID {
				t.Fatal("landing cancelled")
			}
		}
	}
	s = Apply(s, Landed{BatchID: first.ID, NewBaseSHA: first.TestedSHA, NewFence: "next"})
	if s.Active.ID != second.ID {
		t.Fatal("successor not promoted")
	}
	s = Apply(s, forged)
	for _, a := range Plan(cfg, s, at) {
		switch a := a.(type) {
		case RecordCheckCancellation:
			if a.BatchID == first.ID {
				t.Fatal("landed cancelled")
			}
		case CancelChecks:
			if a.BatchID == first.ID {
				t.Fatal("landed cancelled")
			}
		}
	}
}
func TestCancellationDepthOneEmitsNoNewProtocol(t *testing.T) {
	cfg, s, at := chainState(t, 1, 1)
	a := onlyAction[BuildTestRef](t, Plan(cfg, s, at))
	s = Apply(s, BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: at})
	s = Apply(s, TestRefBuilt{BatchID: a.BatchID, TestedSHA: "test", At: at, Method: Merge})
	s = Apply(s, DequeueRequested{PR: 1})
	s = Apply(s, Dequeued{PR: 1, AdmissionID: s.Entries[0].AdmissionID, Reason: Requested})
	for _, a := range Plan(cfg, s, at) {
		switch a.(type) {
		case RecordCheckCancellation, CancelChecks:
			t.Fatal("depth1 cancellation")
		}
	}
}
func TestProvisionalContradictorySnapshotStatusAndTimeout(t *testing.T) {
	cfg, s, at := chainState(t, 4, 2)
	cfg.BatchMaxSize = 2
	s, b := admitChain(t, cfg, s, at)
	// A snapshot made before the prior fix can contain this contradictory pair.
	s.Active.checksPassed = true
	s.Active.ProvisionalFailure = ChecksFailed
	if s.ChecksPassed(b.TestedSHA) {
		t.Fatal("provisional success reported")
	}
	s = Apply(s, BatchFailed{BatchID: b.ID, Reason: ChecksTimeout})
	if s.Active != nil {
		t.Fatal("contradictory success suppressed timeout")
	}
}

func TestCancellationUnsupportedKeepsReadOnlyCompletionIntent(t *testing.T) {
	cfg, s, at := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, at)
	s = observeCancellationRun(s, b)
	s = Apply(s, PRClosed{PR: 1})
	s = Apply(s, BatchesInvalidated(onlyAction[InvalidateBatches](t, Plan(cfg, s, at))))
	request := onlyAction[RecordCheckCancellation](t, Plan(cfg, s, at))
	s = Apply(s, CheckCancellationRequested(request))
	s = Apply(s, CheckCancellationReported{BatchID: b.ID, TestedSHA: b.TestedSHA, Unsupported: true, Detail: "permission"})
	s = snapshotRoundTrip(t, cfg, s, at)
	observe := onlyAction[ObserveRetiredChecks](t, Plan(cfg, s, at))
	for _, a := range Plan(cfg, s, at) {
		if _, ok := a.(CancelChecks); ok {
			t.Fatal("unsupported cancellation repeated remote mutation")
		}
	}
	completed := slices.Clone(observe.Runs)
	completed[0].State = "success"
	s = Apply(s, ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Runs: completed, Terminal: true, Success: true})
	if s.Batch(b.ID) != nil || s.ChecksPassed(b.TestedSHA) {
		t.Fatal("historical completion revived landing evidence")
	}
	s = Apply(s, ActionCompleted{Key: observe.Key()})
	for _, a := range Plan(cfg, s, at) {
		if _, ok := a.(ObserveRetiredChecks); ok {
			t.Fatal("completion observation did not settle")
		}
	}
}

func TestCancellationIncludesNonRequiredObservedWorkflows(t *testing.T) {
	cfg, s, at := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, at)
	s = observeCancellationRun(s, b)
	s = Apply(s, BatchRunsObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Runs: []CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}, {ID: 2, Attempt: 2, Name: "optional", Source: "app:1", State: "pending", WorkflowRunID: 42}}})
	s = snapshotRoundTrip(t, cfg, s, at)
	s = Apply(s, PRClosed{PR: 1})
	s = Apply(s, BatchesInvalidated(onlyAction[InvalidateBatches](t, Plan(cfg, s, at))))
	request := onlyAction[RecordCheckCancellation](t, Plan(cfg, s, at))
	if len(request.Runs) != 2 || request.Runs[1].Name != "optional" || request.Runs[1].Attempt != 2 {
		t.Fatalf("optional workflow omitted: %+v", request.Runs)
	}
}
