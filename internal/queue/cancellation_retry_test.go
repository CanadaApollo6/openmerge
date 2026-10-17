package queue

import (
	"slices"
	"testing"
	"time"
)

func retiredCancellation(t *testing.T) (Config, State, time.Time, CancelChecks) {
	t.Helper()
	cfg, s, at := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, at)
	s = observeCancellationRun(s, b)
	s = Apply(s, PRClosed{PR: 1})
	s = Apply(s, BatchesInvalidated(onlyAction[InvalidateBatches](t, Plan(cfg, s, at))))
	intent := onlyAction[RecordCheckCancellation](t, Plan(cfg, s, at))
	s = Apply(s, CheckCancellationRequested(intent))
	return cfg, s, at, onlyAction[CancelChecks](t, Plan(cfg, s, at))
}

func TestCancellationRetriesBackOffSurviveSnapshotAndExhaust(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "observe"}[readOnly], func(t *testing.T) {
			cfg, s, at, cancel := retiredCancellation(t)
			var action Action = cancel
			if readOnly {
				s = Apply(s, CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Unsupported: true, Detail: "unsupported"})
				action = onlyAction[ObserveRetiredChecks](t, Plan(cfg, s, at))
			}
			for attempt := range CancellationAttemptLimit {
				event, ok := DeferCheckCancellation(action, at, "pending or unavailable")
				if !ok || event.Abandoned != (attempt == CancellationAttemptLimit-1) {
					t.Fatal("wrong attempt budget")
				}
				_, wire, err := EncodeEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeEvent(wire)
				if err != nil {
					t.Fatal(err)
				}
				s = Apply(s, decoded)

				before := slices.Clone(s.effects)
				s = Apply(s, decoded)
				if !slices.EqualFunc(before, s.effects, func(a, b Action) bool { return a.Key() == b.Key() }) {
					t.Fatal("replayed retry advanced its successor")
				}

				s = snapshotRoundTrip(t, cfg, s, at)
				if event.Abandoned {
					break
				}
				delay := min(30*time.Second*time.Duration(1<<attempt), 30*time.Minute)
				wake, ok := NextWake(cfg, s, at)
				if !ok || !wake.Equal(at.Add(delay)) {
					t.Fatalf("wake=%s, want=%s", wake, at.Add(delay))
				}
				for _, planned := range Plan(cfg, s, wake.Add(-time.Nanosecond)) {
					if _, ok := planned.(RetryCheckCancellation); ok {
						t.Fatal("retry claimable during backoff")
					}
				}
				action = onlyAction[RetryCheckCancellation](t, Plan(cfg, s, wake))
				if action.Key() == event.Key {
					t.Fatal("retry reused completed action key")
				}
				at = wake
			}
			for _, effect := range s.effects {
				if _, _, _, ok := cancellationTarget(effect); ok {
					t.Fatal("exhausted cancellation still pending")
				}
			}
		})
	}
}

func TestCancellationAccumulatesIncrementalConfirmations(t *testing.T) {
	cfg, s, at, cancel := retiredCancellation(t)
	second := cancel.Runs[0]
	second.ID, second.Name = 42, "optional"
	// Add a second immutable target before the request is dispatched.
	cancel.Runs = append(slices.Clone(cancel.Runs), second)
	for i, effect := range s.effects {
		if effect.Key() == cancel.Key() {
			s.effects[i] = cancel
		}
	}
	first := cancel.Runs[0]
	first.State = "cancelled"
	s = Apply(s, CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Confirmed: []CheckRun{first}})
	if len(s.cancellationConfirmed[CancelChecks(cancel).Key()]) != 1 {
		t.Fatal("test must hold a retained cancellation confirmation")
	}
	s = snapshotRoundTrip(t, cfg, s, at)
	remaining := onlyAction[CancelChecks](t, Plan(cfg, s, at))
	if len(remaining.Runs) != 1 || remaining.Runs[0].ID != 42 {
		t.Fatal("partial confirmation was not retained")
	}
	second.State = "success"
	s = Apply(s, CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Confirmed: []CheckRun{second}})
	for _, effect := range s.effects {
		if _, _, _, ok := cancellationTarget(effect); ok {
			t.Fatal("incremental confirmations did not settle")
		}
	}
}
