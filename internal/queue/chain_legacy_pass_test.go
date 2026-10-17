package queue

import (
	"testing"
	"time"
)

func assertNoProvisionalLand(t *testing.T, cfg Config, s State, at time.Time) {
	t.Helper()
	if s.Active.ProvisionalFailure == "" {
		return
	}
	for _, action := range Plan(cfg, s, at) {
		if _, ok := action.(Land); ok {
			t.Fatal("planner authorized landing while a provisional failure stands")
		}
	}
	start := LandingStarted{BatchID: s.Active.ID, AttemptID: "unproven", FenceToken: s.Fence, At: at}
	land := Land{BatchID: s.Active.ID, ExpectedBaseSHA: s.Active.BaseSHA, TestedSHA: s.Active.TestedSHA, Ref: s.Active.TestRef, FenceToken: s.Fence, Method: s.Active.Method, PRs: s.Active.PRs, ChecksGeneration: s.Active.checksGeneration}
	if CanStart(cfg, s, land, start) {
		t.Fatal("CanStart authorized provisional landing")
	}
	if Apply(s, start).Active.Phase != Testing {
		t.Fatal("Apply accepted provisional LandingStarted")
	}
}

func TestChainLegacyPassPreservesIdentifiedFailure(t *testing.T) {
	for _, requested := range []bool{false, true} {
		name := "P7-before-request"
		if requested {
			name = "P8-after-request"
		}
		t.Run(name, func(t *testing.T) {
			cfg, s, now := chainState(t, 4, 1)
			s, b := admitChain(t, cfg, s, now)
			s = Apply(s, retryObservation(cfg, s, 1, "failure"))
			if requested {
				s = requestRetry(t, cfg, s, now)
			}
			s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: false})
			s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
			if s.Active.ProvisionalFailure != ChecksFailed || s.Active.CheckRetry == nil || s.Active.checksPassed {
				t.Fatal("aggregate pass resolved an identified provisional failure")
			}
			s = snapshotRoundTrip(t, cfg, s, now)
			assertNoProvisionalLand(t, cfg, s, now)
			if !requested {
				s = requestRetry(t, cfg, s, now)
			}
			s = dispatchRetry(t, cfg, s, now, false)
			assertNoProvisionalLand(t, cfg, s, now)
			// Qualifying newer attempt evidence can still rescue the batch.
			s = Apply(s, retryObservation(cfg, s, 2, "success"))
			if s.Active.ProvisionalFailure != "" {
				t.Fatal("qualifying pass did not resolve the provisional failure")
			}
			onlyAction[Land](t, Plan(cfg, s, now))
		})
	}
}

func TestChainLegacyPassPastRerunDeadlineConfirmsFailure(t *testing.T) {
	cfg, s, now := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, retryObservation(cfg, s, 1, "failure"))
	s = requestRetry(t, cfg, s, now)
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: false})
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	at := s.Active.CheckRetry.Deadline.Add(time.Second)
	// P9: executor preflight re-observes the unchanged failed identity and
	// records a qualifying refresh. No further refresh may postpone timeout.
	s = Apply(s, retryObservation(cfg, s, 1, "failure"))
	s = Apply(s, ChecksRefreshed{BatchID: b.ID, TestedSHA: b.TestedSHA, At: at})
	s = snapshotRoundTrip(t, cfg, s, at)
	assertNoProvisionalLand(t, cfg, s, at)
	confirm := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, at))
	if confirm.Reason != ChecksTimeout {
		t.Fatal("expired rerun did not time out")
	}
	s = Apply(s, ChecksFailureConfirmed(confirm))
	if s.PendingDequeue(1) != ChecksTimeout {
		t.Fatal("timeout did not settle")
	}
}

func TestChainLegacyPassSnapshotRetriesAndTimesOut(t *testing.T) {
	cfg, s, now := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, retryObservation(cfg, s, 1, "failure"))
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: false})
	// Reproduce a snapshot projected before the aggregate-pass fix.
	s.Active.checksPassed = true
	s = snapshotRoundTrip(t, cfg, s, now)
	assertNoProvisionalLand(t, cfg, s, now)
	s = requestRetry(t, cfg, s, now)
	if !s.Active.CheckRetry.Requested {
		t.Fatal("provisional aggregate pass prevented the durable retry request")
	}
	s = dispatchRetry(t, cfg, s, now, false)
	at := s.Active.CheckRetry.Deadline.Add(time.Nanosecond)
	s = Apply(s, ChecksRefreshed{BatchID: b.ID, TestedSHA: b.TestedSHA, At: at})
	assertNoProvisionalLand(t, cfg, s, at)
	confirm := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, at))
	s = Apply(s, ChecksFailureConfirmed(confirm))
	if s.PendingDequeue(1) != ChecksTimeout {
		t.Fatal("provisional aggregate pass prevented durable timeout confirmation")
	}
}
