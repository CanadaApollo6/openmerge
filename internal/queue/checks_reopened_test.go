package queue

import (
	"reflect"
	"testing"
	"time"
)

func TestChecksReopenedRevokesSuccessWithoutExtendingDeadline(t *testing.T) {
	s := queued(1)
	s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
	deadline := epoch.Add(testConfig.CheckTimeout)
	for _, at := range []time.Time{epoch.Add(time.Second), deadline.Add(time.Second)} {
		s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true, At: at})
		onlyAction[Land](t, Plan(testConfig, s, at))
		s = applySerial(s, ChecksReopened{TestedSHA: "tested", At: at})
		if s.Active.checksPassed || s.Active.StartedTestingAt != epoch {
			t.Fatalf("reopen retained success or changed deadline: %+v", s.Active)
		}
		refresh := onlyAction[RefreshChecks](t, Plan(testConfig, s, at))
		s = applySerial(s, ChecksRefreshed{BatchID: refresh.BatchID, TestedSHA: refresh.TestedSHA, At: at})
	}
	if d := onlyAction[Dequeue](t, Plan(testConfig, s, deadline.Add(time.Second))); d.Reason != ChecksTimeout {
		t.Fatalf("rerun escaped original deadline: %+v", d)
	}
}

func TestCheckObservationsIgnoreOlderEvidence(t *testing.T) {
	early := epoch.Add(time.Second)
	latest := epoch.Add(testConfig.CheckTimeout + time.Second)
	passed := ChecksCompleted{TestedSHA: "tested1", Success: true, At: latest}
	reopened := ChecksReopened{TestedSHA: "tested1", At: latest}
	refreshed := ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: latest}
	for _, tc := range []struct {
		name    string
		current []Event
		delayed []Event
		passed  bool
	}{
		{"reopen after newer pass and refresh", []Event{passed, refreshed}, []Event{ChecksReopened{TestedSHA: "tested1", At: early}, ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: early}}, true},
		{"reopen after newer pass before refresh", []Event{passed}, []Event{ChecksReopened{TestedSHA: "tested1", At: early}}, true},
		{"failure after newer pass", []Event{passed, refreshed}, []Event{ChecksCompleted{TestedSHA: "tested1", At: early}}, true},
		{"pass after newer reopen", []Event{reopened, refreshed}, []Event{ChecksCompleted{TestedSHA: "tested1", Success: true, At: early}}, false},
		{"pass after newer reopen before refresh", []Event{reopened}, []Event{ChecksCompleted{TestedSHA: "tested1", Success: true, At: early}}, false},
		{"pass after newer refresh", []Event{refreshed}, []Event{ChecksCompleted{TestedSHA: "tested1", Success: true, At: early}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testingState()
			for _, e := range tc.current {
				s = applySerial(s, e)
			}
			before := Plan(testConfig, s, latest)
			batch := *s.Active
			for _, e := range tc.delayed {
				_, wire, err := EncodeEvent(e)
				if err != nil {
					t.Fatal(err)
				}
				replayed, err := DecodeEvent(wire)
				if err != nil {
					t.Fatal(err)
				}
				s = applySerial(s, replayed)
			}
			if s.Active.checksPassed != tc.passed || !reflect.DeepEqual(*s.Active, batch) || !reflect.DeepEqual(Plan(testConfig, s, latest), before) {
				t.Fatalf("delayed observation changed current evidence: batch=%+v plan=%v, before=%v", s.Active, descriptions(Plan(testConfig, s, latest)), descriptions(before))
			}
		})
	}
}

func TestRetainedChecksOrderByObservationTime(t *testing.T) {
	for _, success := range []bool{false, true} {
		s := queued(1)
		latest := epoch.Add(time.Second)
		var current, delayed Event
		if success {
			current = ChecksCompleted{TestedSHA: "early", Success: true, At: latest}
			delayed = ChecksReopened{TestedSHA: "early", At: epoch}
		} else {
			current = ChecksReopened{TestedSHA: "early", At: latest}
			delayed = ChecksCompleted{TestedSHA: "early", Success: true, At: epoch}
		}
		s = applySerial(applySerial(s, current), delayed)
		s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", Method: Merge, At: epoch})
		if s.Active.checksPassed != success || !s.Active.checksObservedAt.Equal(latest) {
			t.Fatalf("retained observation was reordered: %+v", s.Active)
		}
		if success {
			onlyAction[Land](t, Plan(testConfig, s, latest))
		} else {
			onlyAction[RefreshChecks](t, Plan(testConfig, s, latest))
		}
	}
}

func TestEqualTimeCheckObservationsFollowEventOrder(t *testing.T) {
	s := testingState()
	s = applySerial(s, ChecksCompleted{TestedSHA: "tested1", Success: true, At: epoch})
	s = applySerial(s, ChecksReopened{TestedSHA: "tested1", At: epoch})
	onlyAction[RefreshChecks](t, Plan(testConfig, s, epoch))
	s = applySerial(s, ChecksCompleted{TestedSHA: "tested1", Success: true, At: epoch})
	onlyAction[Land](t, Plan(testConfig, s, epoch))
}

func TestChecksCompletedWireValidation(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"tested_sha":"tested","success":true}`,
		`{"tested_sha":"tested","at":"2026-10-15T12:00:00Z"}`,
		`{"success":true,"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":" ","success":true,"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":"tested","success":null,"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":"tested","success":true,"at":null}`,
		`{"tested_sha":"tested","success":true,"at":"0001-01-01T00:00:00Z"}`,
		`{"tested_sha":"tested","success":true,"at":"bad"}`,
		`{"tested_sha":"tested","success":true,"at":"2026-10-15T12:00:00Z","extra":true}`,
	} {
		if _, err := DecodeEvent([]byte(`{"version":1,"type":"checks_completed","data":` + data + `}`)); err == nil {
			t.Errorf("accepted invalid completion: %s", data)
		}
	}
	for _, e := range []ChecksCompleted{{TestedSHA: " ", At: epoch}, {TestedSHA: "tested"}} {
		if _, _, err := EncodeEvent(e); err == nil {
			t.Errorf("encoded invalid completion: %+v", e)
		}
	}
}

func TestChecksReopenedDropsRetainedResults(t *testing.T) {
	for _, success := range []bool{false, true} {
		s := queued(1)
		s = applySerial(s, ChecksCompleted{TestedSHA: "early", Success: success, At: epoch})
		s = applySerial(s, ChecksReopened{TestedSHA: "early", At: epoch})
		s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", Method: Merge, At: epoch})
		onlyAction[RefreshChecks](t, Plan(testConfig, s, epoch))
	}
}

func TestChecksReopenedCorrelatesSHAAndPreservesLanding(t *testing.T) {
	s := queued(1)
	s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
	s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true, At: epoch})
	s = applySerial(s, ChecksReopened{TestedSHA: "other", At: epoch})
	onlyAction[Land](t, Plan(testConfig, s, epoch))
	s = applySerial(s, LandingStarted{BatchID: s.Active.ID, AttemptID: "attempt", FenceToken: s.Fence, At: epoch})
	s = applySerial(s, ChecksReopened{TestedSHA: "tested", At: epoch})
	if s.Active.Phase != Landing || !s.Active.checksPassed {
		t.Fatal("reopen changed an in-flight landing")
	}
}

func TestChecksReopenedWireValidation(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"tested_sha":"tested"}`, `{"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":null,"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":"","at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":"tested","at":null}`,
		`{"tested_sha":"tested","at":"0001-01-01T00:00:00Z"}`,
		`{"tested_sha":"tested","at":"bad"}`,
		`{"tested_sha":"tested","at":"2026-10-15T12:00:00Z","extra":true}`,
	} {
		if _, err := DecodeEvent([]byte(`{"version":1,"type":"checks_reopened","data":` + data + `}`)); err == nil {
			t.Errorf("accepted invalid reopen: %s", data)
		}
	}
	for _, e := range []ChecksReopened{{TestedSHA: " ", At: epoch}, {TestedSHA: "tested"}} {
		if _, _, err := EncodeEvent(e); err == nil {
			t.Errorf("encoded invalid reopen: %+v", e)
		}
	}
}
