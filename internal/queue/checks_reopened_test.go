package queue

import (
	"testing"
	"time"
)

func TestChecksReopenedRevokesSuccessWithoutExtendingDeadline(t *testing.T) {
	s := queued(1)
	s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
	deadline := epoch.Add(testConfig.CheckTimeout)
	for _, at := range []time.Time{epoch.Add(time.Second), deadline.Add(time.Second)} {
		s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true})
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

func TestChecksReopenedDropsRetainedResults(t *testing.T) {
	for _, success := range []bool{false, true} {
		s := queued(1)
		s = applySerial(s, ChecksCompleted{TestedSHA: "early", Success: success})
		s = applySerial(s, ChecksReopened{TestedSHA: "early", At: epoch})
		s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", Method: Merge, At: epoch})
		onlyAction[RefreshChecks](t, Plan(testConfig, s, epoch))
	}
}

func TestChecksReopenedCorrelatesSHAAndPreservesLanding(t *testing.T) {
	s := queued(1)
	s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
	s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true})
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
