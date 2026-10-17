package queue

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestCancellationOverlapRejectsForeignIdentityAndPrunesProgress(t *testing.T) {
	for _, finish := range []string{"confirmed", "unsupported", "exhausted"} {
		t.Run(finish, func(t *testing.T) {
			cfg, s, at, cancel := retiredCancellation(t)
			second := cancel.Runs[0]
			second.ID, second.Name = 42, "optional"
			cancel.Runs = append(slices.Clone(cancel.Runs), second)
			for i, effect := range s.effects {
				if effect.Key() == cancel.Key() {
					s.effects[i] = cancel
				}
			}
			first := cancel.Runs[0]
			first.State = "cancelled"
			report := CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Confirmed: []CheckRun{first}}
			s = Apply(s, report)
			s = snapshotRoundTrip(t, cfg, s, at)
			before := s
			foreign := second
			foreign.ID, foreign.State = 999, "cancelled"
			bad := report
			bad.Confirmed = []CheckRun{first, foreign}
			bad.Unsupported, bad.Detail = true, "foreign report"
			s = Apply(s, bad)
			if !reflect.DeepEqual(before.effects, s.effects) || !reflect.DeepEqual(before.cancellationConfirmed, s.cancellationConfirmed) {
				t.Fatal("foreign confirmation changed cancellation progress")
			}
			if finish == "exhausted" {
				for range CancellationAttemptLimit {
					for _, a := range Plan(cfg, s, at) {
						if deferred, ok := DeferCheckCancellation(a, at, "missing"); ok {
							s = Apply(s, deferred)
							break
						}
					}
					s = snapshotRoundTrip(t, cfg, s, at)
					at, _ = NextWake(cfg, s, at)
				}
			} else {
				second.State = "cancelled"
				report.Confirmed = []CheckRun{first, second}
				if finish == "unsupported" {
					report.Confirmed = []CheckRun{first}
					report.Unsupported, report.Detail = true, "permission"
				}
				s = Apply(s, report)
			}
			if len(s.cancellationConfirmed) != 0 {
				t.Fatal("settled mutation retained confirmation history")
			}
			if len(before.cancellationConfirmed[cancel.Key()]) != 1 {
				t.Fatal("later fold mutated earlier confirmation state")
			}
		})
	}
}

func TestCancellationSnapshotRejectsMalformedConfirmationProgress(t *testing.T) {
	_, s, _, cancel := retiredCancellation(t)
	confirmed := cancel.Runs[0]
	confirmed.ID, confirmed.Name, confirmed.State = 42, "optional", "cancelled"
	s.cancellationConfirmed = map[string][]CheckRun{cancel.Key(): {confirmed}}
	data, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"orphan", "pending", "overlap", "duplicate", "empty"} {
		t.Run(mode, func(t *testing.T) {
			var wire stateSnapshot
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			runs := wire.CancellationConfirmed[cancel.Key()]
			switch mode {
			case "orphan":
				wire.CancellationConfirmed = map[string][]CheckRun{"foreign": runs}
			case "pending":
				runs[0].State = "pending"
			case "overlap":
				runs[0] = cancel.Runs[0]
				runs[0].State = "cancelled"
			case "duplicate":
				wire.CancellationConfirmed[cancel.Key()] = append(runs, runs[0])
			case "empty":
				wire.CancellationConfirmed[cancel.Key()] = nil
			}
			bad, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(bad); err == nil {
				t.Fatal("accepted malformed confirmation state")
			}
		})
	}
}

func TestRound2OverlappingCancellationConfirmations(t *testing.T) {
	for _, mode := range []string{"duplicate-only", "overlap", "overlap-unsupported"} {
		t.Run(mode, func(t *testing.T) {
			cfg, s, at, cancel := retiredCancellation(t)
			second := cancel.Runs[0]
			second.ID, second.Name = 42, "optional"
			cancel.Runs = append(slices.Clone(cancel.Runs), second)
			for i, effect := range s.effects {
				if effect.Key() == cancel.Key() {
					s.effects[i] = cancel
				}
			}
			first := cancel.Runs[0]
			first.State = "cancelled"
			report := CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Confirmed: []CheckRun{first}}
			s = Apply(s, report)
			s = snapshotRoundTrip(t, cfg, s, at)
			if mode == "duplicate-only" {
				before := s
				s = Apply(s, report)
				if !reflect.DeepEqual(before.effects, s.effects) {
					t.Fatal("exact duplicate changed pending effects")
				}
				return
			}
			if mode == "overlap" {
				second.State = "cancelled"
				report.Confirmed = append(report.Confirmed, second)
			} else {
				report.Unsupported, report.Detail = true, "permission revoked"
			}
			s = Apply(s, report)
			for _, action := range Plan(cfg, s, at) {
				if a, ok := action.(CancelChecks); ok {
					t.Fatalf("overlapping report ignored; cancellation still pending for %+v", a.Runs)
				}
			}
		})
	}
}

func TestRound2RetryReplayAndLateEvents(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		cfg, s, at, cancel := retiredCancellation(t)
		var action Action = cancel
		if readOnly {
			s = Apply(s, CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Unsupported: true, Detail: "unsupported"})
			action = onlyAction[ObserveRetiredChecks](t, Plan(cfg, s, at))
		}
		var history []CheckCancellationDeferred
		for attempt := range CancellationAttemptLimit {
			e, _ := DeferCheckCancellation(action, at, "unavailable")
			history = append(history, e)
			s = Apply(s, e)
			before := s
			for _, late := range history {
				late.At = at.Add(100 * time.Hour)
				s = Apply(s, late)
			}
			if !reflect.DeepEqual(before.effects, s.effects) {
				t.Fatal("a duplicate failure changed the schedule")
			}
			s = snapshotRoundTrip(t, cfg, s, at)
			if attempt == CancellationAttemptLimit-1 {
				break
			}
			at, _ = NextWake(cfg, s, at)
			action = onlyAction[RetryCheckCancellation](t, Plan(cfg, s, at))
		}
		s = Apply(s, CheckCancellationRequested(cancel))
		s = Apply(s, CheckCancellationReported{BatchID: cancel.BatchID, TestedSHA: cancel.TestedSHA, Unsupported: true, Detail: "late unsupported"})
		for _, effect := range s.effects {
			if _, _, _, ok := cancellationTarget(effect); ok {
				t.Fatal("abandoned cancellation resurrected")
			}
		}
	}
}
