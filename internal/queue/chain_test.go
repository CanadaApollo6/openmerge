package queue

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func chainState(t *testing.T, depth, changes int) (Config, State, time.Time) {
	t.Helper()
	now := time.Unix(1, 0).UTC()
	cfg := Config{MergeMethod: Merge, SpeculationMaxDepth: depth, CheckTimeout: time.Hour, RefreshInterval: time.Minute}
	s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	for i := 1; i <= changes; i++ {
		s = Apply(s, PRObserved{PR: i, HeadSHA: fmt.Sprint("h", i), Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: i, At: now})
	}
	return cfg, s, now
}
func admitChain(t *testing.T, cfg Config, s State, now time.Time) (State, *Batch) {
	t.Helper()
	for _, action := range Plan(cfg, s, now) {
		if a, ok := action.(BuildChainedTestRef); ok {
			start := ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now, ParentBatchID: a.ParentBatchID, Position: a.Position}
			if !CanStart(cfg, s, a, start) {
				t.Fatal("admission refused")
			}
			s = Apply(s, start)
			sha := "tested-" + a.BatchID
			s = Apply(s, ChainedTestRefBuilt{BatchID: a.BatchID, TestedSHA: sha, LandingSHA: sha, At: now, Method: Merge, ParentBatchID: a.ParentBatchID, Position: a.Position})
			return s, s.Batch(a.BatchID)
		}
	}
	t.Fatalf("no chain admission: %#v", Plan(cfg, s, now))
	return s, nil
}
func settleChain(s State, b *Batch, now time.Time) State {
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: b.ID, AttemptID: "attempt-" + b.ID, FenceToken: s.Fence, At: now})
	return Apply(s, Landed{BatchID: b.ID, NewBaseSHA: b.LandingCommit(), NewFence: "fence-" + b.ID})
}
func TestChainWindowAndOrderedSettlement(t *testing.T) {
	cfg, s, now := chainState(t, 4, 6)
	var batches []*Batch
	for range 4 {
		var b *Batch
		s, b = admitChain(t, cfg, s, now)
		batches = append(batches, b)
	}
	for i, b := range batches {
		if i > 0 && (b.BaseSHA != batches[i-1].LandingCommit() || b.ParentBatchID != batches[i-1].ID) {
			t.Fatal("broken chain")
		}
		s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	}
	for _, a := range Plan(cfg, s, now) {
		switch a := a.(type) {
		case BuildChainedTestRef:
			t.Fatal("passed batch freed window slot")
		case Land:
			if a.BatchID != batches[0].ID {
				t.Fatal("out of order landing")
			}
		}
	}
	s = Apply(s, LandingStarted{BatchID: batches[0].ID, AttemptID: "attempt", FenceToken: s.Fence, At: now})
	before := s
	s = Apply(s, BaseMoved{SHA: batches[0].LandingCommit()})
	if s.BaseSHA != before.BaseSHA || s.Active.ID != before.Active.ID {
		t.Fatal("own push settled by observation")
	}
	for _, a := range Plan(cfg, s, now) {
		if _, ok := a.(Land); ok {
			t.Fatal("unsettled predecessor allowed landing")
		}
	}
	s = Apply(s, Landed{BatchID: batches[0].ID, NewBaseSHA: batches[0].LandingCommit(), NewFence: "next"})
	if s.Active.ID != batches[1].ID {
		t.Fatal("head not promoted")
	}
	found := false
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(Land); ok {
			found = a.BatchID == batches[1].ID && a.ExpectedBaseSHA == s.BaseSHA && a.FenceToken == "next"
		}
	}
	if !found {
		t.Fatal("settled successor cannot land")
	}
}
func TestChainProvisionalConflictNeverBlamesSuffix(t *testing.T) {
	cfg, s, now := chainState(t, 3, 3)
	s, first := admitChain(t, cfg, s, now)
	// Admit b2 without a build result, then report its conflict.
	var second BuildChainedTestRef
	for _, a := range Plan(cfg, s, now) {
		if b, ok := a.(BuildChainedTestRef); ok {
			second = b
		}
	}
	s = Apply(s, ChainedBatchStarted{BatchID: second.BatchID, BaseSHA: second.BaseSHA, PRs: second.PRs, At: now, ParentBatchID: second.ParentBatchID, Position: second.Position})
	s = Apply(s, TestRefBuildFailed{BatchID: second.BatchID, Conflict: true, At: now})
	if s.PendingDequeue(2) != "" || s.Batch(second.BatchID).ProvisionalFailure != Conflict {
		t.Fatal("provisional conflict blamed a change")
	}
	// Confirm a head failure, invalidating the dependent conflict.
	s = Apply(s, ChecksFailureConfirmed{BatchID: first.ID, TestedSHA: first.TestedSHA, Generation: first.checksGeneration, Reason: ChecksFailed})
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(InvalidateBatches); ok {
			s = Apply(s, BatchesInvalidated(a))
			break
		}
	}
	if s.Batch(second.BatchID) != nil || s.PendingDequeue(2) != "" {
		t.Fatal("invalidated provisional failure blamed a change")
	}
	s = Apply(s, ChecksCompleted{TestedSHA: "tested-" + second.BatchID, Success: true})
	if s.Batch(second.BatchID) != nil {
		t.Fatal("late pass revived invalidated batch")
	}
}
func TestChainProvisionalFailurePromotesAfterSettlement(t *testing.T) {
	cfg, s, now := chainState(t, 3, 2)
	s, first := admitChain(t, cfg, s, now)
	var second BuildChainedTestRef
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(BuildChainedTestRef); ok {
			second = a
		}
	}
	if second.BatchID == "" {
		t.Fatal("no successor build")
	}
	s = Apply(s, ChainedBatchStarted{BatchID: second.BatchID, BaseSHA: second.BaseSHA, PRs: second.PRs, At: now, ParentBatchID: second.ParentBatchID, Position: second.Position})
	s = Apply(s, TestRefBuildFailed{BatchID: second.BatchID, Conflict: true, At: now})
	if s.PendingDequeue(2) != "" {
		t.Fatal("provisional failure blamed change")
	}
	s = settleChain(s, first, now)
	var diagnosed bool
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(DiagnoseBatch); ok {
			s = Apply(s, BatchDiagnosed(a))
			diagnosed = true
		}
	}
	if !diagnosed || s.PendingDequeue(2) != Conflict {
		t.Fatal("settled oldest was not diagnosed")
	}
}
func TestChainInvalidationPreservesLandingAndPurity(t *testing.T) {
	cfg, s, now := chainState(t, 3, 3)
	s, first := admitChain(t, cfg, s, now)
	s, second := admitChain(t, cfg, s, now)
	s, _ = admitChain(t, cfg, s, now)
	s = Apply(s, ChecksCompleted{TestedSHA: first.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: first.ID, AttemptID: "attempt", FenceToken: s.Fence, At: now})
	before := cloneState(s)
	invalid := Apply(s, DequeueRequested{PR: second.PRs[0].PR})
	if !reflect.DeepEqual(s, before) {
		t.Fatal("fold mutated prior state")
	}
	if invalid.Active.Phase != Landing || invalid.Active.InvalidationReason != "" {
		t.Fatal("discarded durable landing")
	}
	for _, a := range Plan(cfg, invalid, now) {
		if a, ok := a.(InvalidateBatches); ok {
			invalid = Apply(invalid, BatchesInvalidated(a))
			break
		}
	}
	if len(invalid.Batches()) != 1 || len(invalid.Invalidations) != 1 {
		t.Fatal("suffix was not invalidated")
	}
}
func TestChainSuffixRetryWaitsForOldest(t *testing.T) {
	cfg, s, now := chainState(t, 3, 2)
	s, first := admitChain(t, cfg, s, now)
	s, second := admitChain(t, cfg, s, now)
	obs := ChecksObserved{BatchID: second.ID, TestedSHA: second.TestedSHA, Terminal: true, Success: false, Timeout: cfg.CheckTimeout, OriginalDeadline: now.Add(cfg.CheckTimeout), Runs: []CheckRun{{Name: "ci", Source: "fake", ID: 1, Attempt: 1, State: "failure"}}}
	s = Apply(s, obs)
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(RecordCheckRerun); ok && a.BatchID == second.ID {
			t.Fatal("provisional failure consumed retry")
		}
	}
	s = settleChain(s, first, now)
	found := false
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(RecordCheckRerun); ok && a.BatchID == second.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("oldest did not automatically retry at depth > 1")
	}
}

func TestLateChainBuildReissuesCleanupAfterCompletedDeletion(t *testing.T) {
	cfg, s, now := chainState(t, 4, 2)
	s, _ = admitChain(t, cfg, s, now)
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, DequeueRequested{PR: 2})
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(InvalidateBatches); ok {
			s = Apply(s, BatchesInvalidated(a))
			break
		}
	}
	deletion := DeleteRef{Ref: b.TestRef}
	s = Apply(s, ActionCompleted{Key: deletion.Key()})
	s = Apply(s, ChainedTestRefBuilt{BatchID: b.ID, TestedSHA: b.TestedSHA, LandingSHA: b.LandingSHA, ParentBatchID: b.ParentBatchID, Position: b.ChainPosition, Method: b.Method, At: now})
	found := false
	for _, a := range Plan(cfg, s, now) {
		if cleanup, ok := a.(CleanupInvalidatedRef); ok && cleanup.Ref == b.TestRef && cleanup.Key() != deletion.Key() {
			found = true
		}
	}
	if !found || s.Batch(b.ID) != nil {
		t.Fatal("late builder revived batch or failed to reissue cleanup")
	}
}

func TestConfigChangeDropsAllOldBisectionGroups(t *testing.T) {
	cfg, s, now := chainState(t, 4, 8)
	cfg.BatchMaxSize = 4
	s, b := admitChain(t, cfg, s, now)
	s = Apply(s, ChecksFailureConfirmed{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Reason: ChecksFailed})
	s, first := admitChain(t, cfg, s, now)
	s, second := admitChain(t, cfg, s, now)
	if !first.FromBisection || !second.FromBisection {
		t.Fatal("no halves")
	}
	// An already-invalid half must not survive a subsequent config change.
	s = Apply(s, DequeueRequested{PR: first.PRs[0].PR})
	s = Apply(s, ConfigApplied{Commit: "policy", Digest: fmt.Sprintf("%064x", 1)})
	cfg.ConfigDigest = s.ConfigDigest
	for i := 0; i < 4; i++ {
		if a, ok := s.pendingInvalidation(); ok {
			s = Apply(s, BatchesInvalidated(a))
		}
	}
	if s.Active != nil || len(s.pending) != 0 {
		t.Fatal("old policy retained bisection groups")
	}
}
func TestExternalBaseMoveAndAbandonedPredecessorInvalidateSuffix(t *testing.T) {
	for _, scenario := range []string{"outside push", "abandon"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, s, now := chainState(t, 4, 3)
			s, head := admitChain(t, cfg, s, now)
			s, tail := admitChain(t, cfg, s, now)
			if scenario == "abandon" {
				s = Apply(s, ChecksCompleted{TestedSHA: head.TestedSHA, Success: true})
				s = Apply(s, LandingStarted{BatchID: head.ID, AttemptID: "attempt", FenceToken: s.Fence, At: now})
				s = Apply(s, LandingAbandoned{BatchID: head.ID, NewFence: "abandoned"})
			} else {
				s = Apply(s, BaseMoved{SHA: "outside"})
			}
			if b := s.Batch(tail.ID); b == nil || b.InvalidationReason == "" {
				t.Fatal("dependent survived invalid predecessor")
			}
			for i := 0; i < 4; i++ {
				if a, ok := s.pendingInvalidation(); ok {
					s = Apply(s, BatchesInvalidated(a))
				}
			}
			if s.Batch(tail.ID) != nil || s.PendingDequeue(2) != "" {
				t.Fatal("invalidated dependent was blamed")
			}
			s, next := admitChain(t, cfg, s, now)
			if next.ID == head.ID || next.ID == tail.ID || next.BaseSHA != s.BaseSHA {
				t.Fatal("did not rebuild chain with fresh ID on settled base")
			}
		})
	}
}

func TestChainBackoffWakesAfterLastBuilderFails(t *testing.T) {
	cfg, s, now := chainState(t, 4, 1)
	cfg.BuildBackoff = time.Second
	a := Plan(cfg, s, now)[0].(BuildChainedTestRef)
	s = Apply(s, ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now, Position: 1})
	s = Apply(s, TestRefBuildFailed{BatchID: a.BatchID, At: now})
	if s.Active != nil {
		t.Fatal("failed builder not removed")
	}
	wake, ok := NextWake(cfg, s, now)
	if !ok || !wake.Equal(now.Add(time.Second)) {
		t.Fatal("empty chain lost build backoff wake")
	}
}
