package queue

import (
	"fmt"
	"reflect"
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

// Fold the wire representation as well as the in-memory events: replay must use
// durable append order regardless of a reader's clock.
func foldCheckEvents(t *testing.T, s State, events ...Event) State {
	t.Helper()
	for _, e := range events {
		_, wire, err := EncodeEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(wire)
		if err != nil {
			t.Fatal(err)
		}
		s = Apply(s, decoded)
	}
	return s
}

func TestCheckObservationsFollowLogOrder(t *testing.T) {
	for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			at := epoch.Add(testConfig.CheckTimeout + time.Second)
			pass := ChecksCompleted{TestedSHA: "tested1", Success: true}
			refresh := ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: at}
			reopen := ChecksReopened{TestedSHA: "tested1", At: at.Add(offset)}
			events := []Event{pass, refresh, reopen}
			pending := foldCheckEvents(t, testingState(), events...)
			onlyAction[RefreshChecks](t, Plan(testConfig, pending, at))
			if !reflect.DeepEqual(pending, foldCheckEvents(t, testingState(), events...)) {
				t.Fatal("wire replay changed the projection")
			}
			// A delayed pass may restore projected success. Land's fresh preflight is
			// responsible for declining it if the forge still reports pending checks.
			passed := foldCheckEvents(t, pending, pass)
			onlyAction[Land](t, Plan(testConfig, passed, at))
			failed := foldCheckEvents(t, passed, ChecksCompleted{TestedSHA: "tested1", Success: false})
			if d := onlyAction[Dequeue](t, Plan(testConfig, failed, at)); d.Reason != ChecksFailed {
				t.Fatalf("failure did not close the gate: %+v", d)
			}
		})
	}
}

func TestRetainedChecksFollowLogOrder(t *testing.T) {
	for _, pendingLast := range []bool{false, true} {
		for _, at := range []time.Time{epoch.Add(-time.Second), epoch, epoch.Add(time.Second)} {
			s := queued(1)
			pass := ChecksCompleted{TestedSHA: "early", Success: true}
			reopen := ChecksReopened{TestedSHA: "early", At: at}
			events := []Event{reopen, pass}
			if pendingLast {
				events = []Event{pass, reopen}
			}
			s = foldCheckEvents(t, s, events...)
			s = foldCheckEvents(t, s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", Method: Merge, At: epoch})
			if pendingLast {
				onlyAction[RefreshChecks](t, Plan(testConfig, s, epoch))
			} else {
				onlyAction[Land](t, Plan(testConfig, s, epoch))
			}
		}
	}
}

func TestReopenInvalidatesTimeoutEvidence(t *testing.T) {
	for _, size := range []int{1, 2} {
		for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
			t.Run(fmt.Sprintf("size=%d/offset=%s", size, offset), func(t *testing.T) {
				cfg := batchConfig(size)
				cfg.CheckTimeout = testConfig.CheckTimeout
				s := startPlanned(t, cfg, batchQueue(size))
				s = foldCheckEvents(t, s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
				deadline := epoch.Add(cfg.CheckTimeout)
				now := deadline.Add(time.Second)
				s = foldCheckEvents(t, s,
					ChecksCompleted{TestedSHA: "tested", Success: true},
					ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: now},
					ChecksReopened{TestedSHA: "tested", At: now.Add(offset)})
				first := onlyAction[RefreshChecks](t, Plan(cfg, s, now))
				// Even a refresh appended after the reopen is insufficient if its read
				// predates the deadline. It still advances the durable action key.
				s = foldCheckEvents(t, s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: epoch})
				next := onlyAction[RefreshChecks](t, Plan(cfg, s, now))
				if next.Key() == first.Key() {
					t.Fatal("refresh reused the completed key")
				}
				s = foldCheckEvents(t, s,
					ChecksReopened{TestedSHA: "tested", At: now},
					ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: now})
				if size == 1 {
					if d := onlyAction[Dequeue](t, Plan(cfg, s, now)); d.Reason != ChecksTimeout {
						t.Fatalf("wrong dequeue: %+v", d)
					}
				} else {
					if f := onlyAction[FailBatch](t, Plan(cfg, s, now)); f.Reason != ChecksTimeout {
						t.Fatalf("wrong batch failure: %+v", f)
					}
				}
			})
		}
	}
}

func TestBisectionChildCheckObservationsFollowLogOrder(t *testing.T) {
	cfg := batchConfig(4)
	s := startPlanned(t, cfg, batchQueue(4))
	s = foldCheckEvents(t, s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "parent", Method: Merge, At: epoch}, ChecksCompleted{TestedSHA: "parent", Success: false})
	s = startPlanned(t, cfg, s)
	s = foldCheckEvents(t, s,
		ChecksCompleted{TestedSHA: "parent", Success: true},
		ChecksReopened{TestedSHA: "parent", At: epoch.Add(time.Hour)},
		ChecksReopened{TestedSHA: "child", At: epoch.Add(3 * time.Second)},
		ChecksCompleted{TestedSHA: "child", Success: true},
		TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "child", Method: Merge, At: epoch})
	if !s.Active.FromBisection {
		t.Fatal("lost bisection provenance")
	}
	onlyAction[Land](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, ChecksReopened{TestedSHA: "child", At: epoch})
	onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
}

func TestChecksCompletedWireValidation(t *testing.T) {
	for _, data := range []string{
		`{}`, `{"tested_sha":"tested"}`, `{"success":true}`,
		`{"tested_sha":" ","success":true}`, `{"tested_sha":null,"success":true}`,
		`{"tested_sha":"tested","success":null}`,
		`{"tested_sha":"tested","success":true,"at":"2026-10-15T12:00:00Z"}`,
		`{"tested_sha":"tested","success":true,"extra":true}`,
	} {
		if _, err := DecodeEvent([]byte(`{"version":1,"type":"checks_completed","data":` + data + `}`)); err == nil {
			t.Errorf("accepted invalid completion: %s", data)
		}
	}
	if _, _, err := EncodeEvent(ChecksCompleted{TestedSHA: " "}); err == nil {
		t.Error("encoded an empty tested SHA")
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
