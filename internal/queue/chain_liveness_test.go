package queue

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestChainStaleRefreshesAreBoundedByRecordedDeadline(t *testing.T) {
	checked := 0
	for _, phase := range []string{"pending", "requested", "dispatched"} {
		for _, result := range []string{"failure", "success"} {
			for _, identity := range [][2]uint64{{1, 3}, {2, 1}, {2, 2}} {
				for _, refreshOffset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
					for _, reopened := range []bool{false, true} {
						for _, legacyPass := range []bool{false, true} {
							name := fmt.Sprintf("%s/%s/%v/refresh%v/reopened%v/legacyPass%v", phase, result, identity, refreshOffset, reopened, legacyPass)
							t.Run(name, func(t *testing.T) {
								assertStaleChainDeadline(t, phase, result, identity, refreshOffset, reopened, legacyPass)
							})
							checked++
						}
					}
				}
			}
		}
	}
	t.Logf("checked %d stale-read/deadline combinations", checked)
}

func assertStaleChainDeadline(t *testing.T, phase, result string, identity [2]uint64, refreshOffset time.Duration, reopened, legacyPass bool) {
	t.Helper()
	cfg, s, now := chainState(t, 4, 2)
	s, first := admitChain(t, cfg, s, now)
	s, second := admitChain(t, cfg, s, now)
	observed := ChecksObserved{BatchID: second.ID, TestedSHA: second.TestedSHA, Timeout: cfg.CheckTimeout, OriginalDeadline: now.Add(cfg.CheckTimeout), Runs: []CheckRun{{Name: "ci", Source: "app:ci", ID: 2, Attempt: 2, State: "pending"}}}
	if phase != "pending" {
		observed.Terminal, observed.Runs[0].State = true, "failure"
	}
	s = Apply(s, observed)
	if phase == "pending" {
		s = Apply(s, BatchFailed{BatchID: second.ID, Reason: ChecksTimeout})
	}
	s = settleChain(s, first, now.Add(10*time.Second))
	if phase != "pending" {
		s = requestRetry(t, cfg, s, now.Add(20*time.Second))
		if phase == "dispatched" {
			s = dispatchRetry(t, cfg, s, now.Add(20*time.Second), false)
		}
		s = Apply(s, ChecksCompleted{TestedSHA: second.TestedSHA, Success: false})
	}
	deadline, _ := s.Active.checkDeadline(cfg)
	s = Apply(s, ChecksRefreshed{BatchID: second.ID, TestedSHA: second.TestedSHA, At: deadline.Add(refreshOffset)})
	if reopened {
		s = Apply(s, ChecksReopened{TestedSHA: second.TestedSHA, At: deadline})
	}
	before := *s.Active.CheckRetry
	at := deadline.Add(time.Nanosecond)
	// Even a stale terminal success must not land. A post-deadline
	// refresh may qualify the timeout, never restart the timer.
	stale := observed
	stale.Runs = []CheckRun{{Name: "ci", Source: "app:ci", ID: identity[0], Attempt: identity[1], State: result}}
	stale.Terminal, stale.Success = true, result == "success"
	if phase == "pending" && identity == [2]uint64{2, 2} {
		// An equal pending identity may legitimately finish. Use an
		// older attempt here so this remains a stale-read property.
		stale.Runs[0].Attempt = 1
	}
	for step := range 2 {
		s = Apply(s, stale)
		if legacyPass {
			s = Apply(s, ChecksCompleted{TestedSHA: second.TestedSHA, Success: true})
		}
		r := s.Active.CheckRetry
		if r.Requested != before.Requested || !r.RequestedAt.Equal(before.RequestedAt) || !r.OriginalDeadline.Equal(before.OriginalDeadline) || !r.Deadline.Equal(before.Deadline) || !reflect.DeepEqual(r.FailedRuns, before.FailedRuns) || r.ObservedRuns[0].ID != 2 || r.ObservedRuns[0].Attempt != 2 {
			t.Fatal("stale observation changed identities, retry budget or deadlines")
		}
		s = snapshotRoundTrip(t, cfg, s, at)
		plan := Plan(cfg, s, at)
		assertNoProvisionalLand(t, cfg, s, at)
		if step == 0 && (s.Active.needsRefresh || refreshOffset < 0) {
			refresh := onlyAction[RefreshChecks](t, plan)
			s = Apply(s, ChecksRefreshed{BatchID: refresh.BatchID, TestedSHA: refresh.TestedSHA, At: at})
			continue
		}
		confirm := onlyAction[ConfirmCheckFailure](t, plan)
		if confirm.Reason != ChecksTimeout {
			t.Fatal("stale result authorized failure instead of timeout")
		}
		s = Apply(s, ChecksFailureConfirmed(confirm))
		if s.PendingDequeue(second.PRs[0].PR) != ChecksTimeout {
			t.Fatal("timeout did not settle")
		}
		return
	}
	t.Fatal("more than one refresh past deadline")
}

func TestProvisionalHeadKeepsRetryDeadlineWake(t *testing.T) {
	cfg, s, now := chainState(t, 4, 1)
	cfg.RefreshInterval = 2 * cfg.CheckTimeout
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, retryObservation(cfg, s, 1, "failure"))
	s = requestRetry(t, cfg, s, now)
	s = dispatchRetry(t, cfg, s, now, false)
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: false})
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	s = Apply(s, ChecksRefreshed{BatchID: b.ID, TestedSHA: b.TestedSHA, At: now})
	// Older projections could retain aggregate success alongside the marker.
	// Even that snapshot must keep the planner gate and deadline wake intact.
	s.Active.checksPassed = true
	s = snapshotRoundTrip(t, cfg, s, now)
	assertNoProvisionalLand(t, cfg, s, now)
	if plan := Plan(cfg, s, now); len(plan) != 0 {
		t.Fatalf("expected drained head before deadline, got %#v", plan)
	}
	deadline := s.Active.CheckRetry.Deadline.Add(time.Nanosecond)
	if wake, ok := NextWake(cfg, s, now); !ok || !wake.Equal(deadline) {
		t.Fatalf("provisional head lost deadline wake: %s, %v", wake, ok)
	}
	onlyAction[RefreshChecks](t, Plan(cfg, s, deadline))
}
