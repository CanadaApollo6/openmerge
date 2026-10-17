package queue

import (
	"reflect"
	"testing"
	"time"
)

func failNextChainBuild(t *testing.T, cfg Config, s State, now time.Time) State {
	t.Helper()
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(BuildChainedTestRef); ok {
			s = Apply(s, ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now, ParentBatchID: a.ParentBatchID, Position: a.Position})
			return Apply(s, TestRefBuildFailed{BatchID: a.BatchID, At: now})
		}
	}
	t.Fatal("no builder to fail")
	return s
}

func TestChainInvalidationOrdersMembersBeforeBuildRetry(t *testing.T) {
	cfg, s, now := chainState(t, 4, 3)
	s, _ = admitChain(t, cfg, s, now)
	s = failNextChainBuild(t, cfg, s, now)
	s = Apply(s, BaseMoved{SHA: "outside"})
	a, ok := s.pendingInvalidation()
	if !ok {
		t.Fatal("no invalidation")
	}
	s = Apply(s, BatchesInvalidated(a))
	s, first := admitChain(t, cfg, s, now)
	if first.PRs[0].PR != 1 {
		t.Fatalf("retried PR %d before invalidated PR 1", first.PRs[0].PR)
	}
	_, second := admitChain(t, cfg, s, now)
	if second.PRs[0].PR != 2 {
		t.Fatal("lost pending retry order")
	}
}

func TestChainBuildRetryWaitsForBisectionResolution(t *testing.T) {
	cfg, s, now := chainState(t, 4, 6)
	cfg.BatchMaxSize = 2
	s, head := admitChain(t, cfg, s, now)
	s = failNextChainBuild(t, cfg, s, now)
	// Confirmed oldest failure splits [1,2], ahead of the pending [3,4] retry.
	s = Apply(s, ChecksFailureConfirmed{BatchID: head.ID, TestedSHA: head.TestedSHA, Generation: head.checksGeneration, Reason: ChecksFailed})
	s, left := admitChain(t, cfg, s, now)
	s, right := admitChain(t, cfg, s, now)
	if !left.FromBisection || !right.FromBisection {
		t.Fatal("missing halves")
	}
	assertNoAdmission := func(s State) {
		t.Helper()
		for _, a := range Plan(cfg, s, now) {
			if _, ok := a.(BuildChainedTestRef); ok {
				t.Fatal("retry bypassed unresolved bisection")
			}
		}
	}
	assertNoAdmission(s)
	s = settleChain(s, left, now)
	assertNoAdmission(s)
	s = settleChain(s, right, now)
	_, retry := admitChain(t, cfg, s, now)
	if !reflect.DeepEqual(retry.PRs, []BatchPR{{PR: 3, HeadSHA: "h3"}, {PR: 4, HeadSHA: "h4"}}) {
		t.Fatal("lost retry group")
	}
}

func TestChainLegacyProvisionalFailureRefreshesBeforeRetry(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "supported", false: "unsupported"}[supported], func(t *testing.T) {
			cfg, s, now := chainState(t, 3, 2)
			s, first := admitChain(t, cfg, s, now)
			s, second := admitChain(t, cfg, s, now)
			s = Apply(s, ChecksCompleted{TestedSHA: second.TestedSHA, Success: false})
			s = settleChain(s, first, now)
			var refresh bool
			for _, a := range Plan(cfg, s, now) {
				switch a := a.(type) {
				case DiagnoseBatch:
					t.Fatal("diagnosed legacy failure before refresh/rerun")
				case RefreshChecks:
					refresh = refresh || a.BatchID == second.ID
				}
			}
			if !refresh {
				t.Fatal("oldest legacy failure was not refreshed")
			}
			s = Apply(s, ChecksObserved{BatchID: second.ID, TestedSHA: second.TestedSHA, Terminal: true, Timeout: cfg.CheckTimeout, OriginalDeadline: now.Add(cfg.CheckTimeout), Runs: []CheckRun{{Name: "ci", Source: "fake", ID: 1, Attempt: 1, State: "failure"}}})
			s = requestRetry(t, cfg, s, now)
			s = dispatchRetry(t, cfg, s, now, !supported)
			if supported {
				if s.PendingDequeue(2) != "" {
					t.Fatal("original failure blamed before rerun")
				}
				s = Apply(s, ChecksObserved{BatchID: second.ID, TestedSHA: second.TestedSHA, Terminal: true, Success: true, Timeout: cfg.CheckTimeout, OriginalDeadline: now.Add(cfg.CheckTimeout), Runs: []CheckRun{{Name: "ci", Source: "fake", ID: 1, Attempt: 2, State: "success"}}})
				if !s.Active.checksPassed || s.PendingDequeue(2) != "" {
					t.Fatal("rerun pass failed to rescue provisional change")
				}
			} else {
				for _, a := range Plan(cfg, s, now) {
					if a, ok := a.(ConfirmCheckFailure); ok {
						s = Apply(s, ChecksFailureConfirmed(a))
					}
				}
				if s.PendingDequeue(2) != ChecksFailed {
					t.Fatal("unsupported rerun was not diagnosed")
				}
			}
		})
	}
}

func TestChainLegacyHeadFailureCannotDiagnoseWithoutRetry(t *testing.T) {
	cfg, s, now := chainState(t, 2, 1)
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: false})
	s = Apply(s, BatchDiagnosed{BatchID: b.ID, Reason: ChecksFailed})
	if s.PendingDequeue(1) != "" || s.Active == nil {
		t.Fatal("legacy fallback bypassed rerun")
	}
	onlyAction[RefreshChecks](t, Plan(cfg, s, now))
}
