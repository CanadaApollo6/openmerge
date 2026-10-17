package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func adaptiveState(t *testing.T, depth, changes int) (Config, State, time.Time) {
	t.Helper()
	cfg, s, at := chainState(t, depth, changes)
	cfg.ConfigDigest = strings.Repeat("a", 64)
	s = Apply(s, cfg.Applied("policy", cfg.ConfigDigest))
	return cfg, s, at
}
func confirmOriginal(s State, b *Batch, reason DequeueReason) State {
	return Apply(s, ChecksFailureConfirmed{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Reason: reason})
}
func TestAdaptiveWindowOriginalFailureOnceAndHalfLandings(t *testing.T) {
	for _, reason := range []DequeueReason{ChecksFailed, ChecksTimeout} {
		t.Run(string(reason), func(t *testing.T) {
			cfg, s, at := adaptiveState(t, 8, 8)
			cfg.BatchMaxSize = 4
			s, b := admitChain(t, cfg, s, at)
			s = confirmOriginal(s, b, reason)
			if s.Window(cfg) != 4 {
				t.Fatalf("original failure W=%d", s.Window(cfg))
			}
			f := s.LastWindowFailure()
			s = confirmOriginal(s, b, reason)
			if s.Window(cfg) != 4 || !reflect.DeepEqual(f, s.LastWindowFailure()) {
				t.Fatal("duplicate halved window")
			}
			s = snapshotRoundTrip(t, cfg, s, at)
			s, half := admitChain(t, cfg, s, at)
			if !half.FromBisection || len(half.PRs) != 2 {
				t.Fatal("missing half")
			}
			s = confirmOriginal(s, half, reason)
			if s.Window(cfg) != 4 || !reflect.DeepEqual(f, s.LastWindowFailure()) {
				t.Fatal("half failure halved window")
			}
			s, quarter := admitChain(t, cfg, s, at)
			s = settleChain(s, quarter, at)
			if s.Window(cfg) != 5 {
				t.Fatal("half landing did not grow window")
			}
			s = Apply(s, Landed{BatchID: quarter.ID, NewBaseSHA: quarter.LandingCommit(), NewFence: "duplicate"})
			if s.Window(cfg) != 5 {
				t.Fatal("duplicate landing grew window")
			}
			snapshotRoundTrip(t, cfg, s, at)
		})
	}
}
func TestAdaptiveWindowSingletonDuplicateAndMinimum(t *testing.T) {
	cfg, s, at := adaptiveState(t, 7, 4)
	cfg.SpeculationMinDepth = 2
	// A config transition records bounds; changing the live cfg alone is not state.
	cfg.ConfigDigest = strings.Repeat("b", 64)
	s = Apply(s, cfg.Applied("bounds", cfg.ConfigDigest))
	for _, want := range []int{3, 2, 2} {
		var b *Batch
		s, b = admitChain(t, cfg, s, at)
		before, previousW := s.LastWindowFailure(), s.Window(cfg)
		e := ChecksFailureConfirmed{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Reason: ChecksTimeout}
		s = Apply(s, e)
		f := s.LastWindowFailure()
		if previousW == cfg.SpeculationMinDepth && !reflect.DeepEqual(before, f) {
			t.Fatal("floor failure changed evidence")
		}
		s = Apply(s, e)
		s = Apply(s, BatchFailed{BatchID: b.ID, Reason: ChecksTimeout})
		if s.Window(cfg) != want || !reflect.DeepEqual(f, s.LastWindowFailure()) {
			t.Fatalf("duplicate changed W or last failure: %d", s.Window(cfg))
		}
		d := s.dequeues[b.PRs[0].PR]
		s = Apply(s, Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason, BatchID: d.BatchID})
		if s.Window(cfg) != want {
			t.Fatal("dequeue counted failure again")
		}
	}
}
func TestAdaptiveWindowIdleConfigClampAndNoReset(t *testing.T) {
	cfg, s, at := adaptiveState(t, 8, 1)
	s, b := admitChain(t, cfg, s, at)
	s = confirmOriginal(s, b, ChecksFailed)
	d := s.dequeues[1]
	s = Apply(s, Dequeued{PR: 1, AdmissionID: d.AdmissionID, Reason: d.Reason})
	if s.Active != nil || s.Window(cfg) != 4 {
		t.Fatal("idle reset window")
	}
	cfg.ConfigDigest = strings.Repeat("b", 64)
	cfg.SpeculationMaxDepth = 12
	s = Apply(s, cfg.Applied("higher", cfg.ConfigDigest))
	if s.Window(cfg) != 4 {
		t.Fatal("higher ceiling reset W")
	}
	cfg.ConfigDigest = strings.Repeat("c", 64)
	cfg.SpeculationMinDepth = 6
	s = Apply(s, cfg.Applied("minimum", cfg.ConfigDigest))
	if s.Window(cfg) != 6 {
		t.Fatal("minimum did not clamp W")
	}
	cfg.ConfigDigest = strings.Repeat("d", 64)
	cfg.SpeculationMinDepth = 0
	cfg.SpeculationMaxDepth = 3
	s = Apply(s, cfg.Applied("lower", cfg.ConfigDigest))
	if s.Window(cfg) != 3 {
		t.Fatal("ceiling did not clamp W")
	}
	s = snapshotRoundTrip(t, cfg, s, at)
	cfg.ConfigDigest = strings.Repeat("e", 64)
	cfg.SpeculationMaxDepth = 1
	s = Apply(s, cfg.Applied("serial", cfg.ConfigDigest))
	if s.Window(cfg) != 1 {
		t.Fatal("downgrade did not clamp")
	}
	cfg.ConfigDigest = strings.Repeat("f", 64)
	cfg.SpeculationMaxDepth = 8
	s = Apply(s, cfg.Applied("upgrade", cfg.ConfigDigest))
	if s.Window(cfg) != 1 {
		t.Fatal("upgrade reset existing W")
	}
}
func TestAdaptiveWindowShrinkRetainsAdmittedBatches(t *testing.T) {
	cfg, s, at := adaptiveState(t, 8, 12)
	var head *Batch
	for range 6 {
		var b *Batch
		s, b = admitChain(t, cfg, s, at)
		if head == nil {
			head = b
		}
	}
	cfg.SpeculationMaxDepth = 2
	cfg.ConfigDigest = strings.Repeat("b", 64)
	s = Apply(s, ChecksCompleted{TestedSHA: head.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: head.ID, AttemptID: "in-flight", FenceToken: s.Fence, At: at})
	// Clamping itself never discards work. Config's independent invalidation rule
	// still marks the unstarted suffix; a durable landing remains intact.
	before := s.Batches()
	s = Apply(s, cfg.Applied("smaller", cfg.ConfigDigest))
	if len(s.Batches()) != len(before) || s.Active.Phase != Landing {
		t.Fatal("shrink cancelled admitted work")
	}
	if a := planChainAdmission(cfg, s, at); len(a) != 0 {
		t.Fatal("admitted beyond reduced W")
	}
}
func TestAdaptiveWindowProvisionalInvalidationAndCancellationAreNeutral(t *testing.T) {
	cfg, s, at := adaptiveState(t, 4, 5)
	s, head := admitChain(t, cfg, s, at)
	s, tail := admitChain(t, cfg, s, at)
	s = Apply(s, BatchFailed{BatchID: tail.ID, Reason: ChecksTimeout})
	s = Apply(s, ChecksFailureConfirmed{BatchID: tail.ID, TestedSHA: tail.TestedSHA, Reason: ChecksTimeout})
	if s.Window(cfg) != 4 || s.LastWindowFailure() != nil {
		t.Fatal("provisional failure halved W")
	}
	s = Apply(s, BaseMoved{SHA: "outside"})
	a, _ := s.pendingInvalidation()
	s = Apply(s, BatchesInvalidated(a))
	s = Apply(s, CheckCancellationReported{BatchID: head.ID, TestedSHA: head.TestedSHA, Unsupported: true, Detail: "unsupported"})
	if s.Window(cfg) != 4 || s.LastWindowFailure() != nil {
		t.Fatal("invalidation/cancellation halved W")
	}
}
func TestAdaptiveWindowCountsEveryOpenPhaseAndBlocksAdmission(t *testing.T) {
	cfg, s, at := adaptiveState(t, 8, 8)
	s.window.Current = 2
	s, head := admitChain(t, cfg, s, at)
	s, tail := admitChain(t, cfg, s, at)
	for _, phase := range []Phase{Building, Testing, Landing} {
		local := cloneState(s)
		local.Active.Phase = phase
		local.Active.checksPassed = true
		local.Batch(tail.ID).checksPassed = true
		if len(planChainAdmission(cfg, local, at)) != 0 {
			t.Fatalf("%s or passed waiting batch freed slot", phase)
		}
	}
	// A policy clamp below occupancy only blocks new work; it has no side effects.
	s.window.Current = 1
	before, _ := EncodeSnapshot(s)
	if len(planChainAdmission(cfg, s, at)) != 0 {
		t.Fatal("shrunken window admitted")
	}
	after, _ := EncodeSnapshot(s)
	if !bytes.Equal(before, after) || s.Batch(head.ID) == nil {
		t.Fatal("admission check mutated state")
	}
}
func TestAdaptiveBatchSizeExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		failures, samples int
		target            float64
		want              int
	}{
		{2, 19, .81, 8}, {2, 20, .81, 2}, {2, 20, math.Nextafter(.81, 1), 1},
		{2, 20, math.Nextafter(.81, 0), 2}, {1, 20, .95, 1}, {0, 200, .9999999999999999, 8},
		{200, 200, .8, 1}, {10, 200, .8, 4}, {10, 200, 0, 8},
	} {
		if got := AdaptiveBatchSize(8, tc.failures, tc.samples, tc.target); got != tc.want {
			t.Errorf("%+v got %d", tc, got)
		}
	}
}
func TestAdaptiveOutcomeRingOrderDuplicatesResetAndPendingSizes(t *testing.T) {
	cfg, s, at := adaptiveState(t, 4, 225)
	cfg.BatchTargetPassRate = .8
	for i := 0; i < 221; i++ {
		var b *Batch
		s, b = admitChain(t, cfg, s, at)
		if i%10 == 0 {
			s = confirmOriginal(s, b, ChecksFailed)
			d := s.dequeues[b.PRs[0].PR]
			e := Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason}
			s = Apply(s, e)
			s = Apply(s, e)
		} else {
			s = settleChain(s, b, at)
		}
	}
	failures, n := s.FailureEstimate()
	if failures != 20 || n != 200 || s.outcomes[0].PR != 22 || s.outcomes[199].PR != 221 {
		t.Fatalf("wrong ordered ring %d/%d", failures, n)
	}
	// A conflict dequeue adds nothing.
	s, b := admitChain(t, cfg, s, at)
	s = Apply(s, TestRefBuildFailed{BatchID: b.ID, Conflict: true, At: at})
	s = Apply(s, Dequeued{PR: b.PRs[0].PR, AdmissionID: s.entry(b.PRs[0].PR).AdmissionID, Reason: Conflict})
	if f, n := s.FailureEstimate(); f != failures || n != 200 {
		t.Fatal("conflict entered estimate")
	}
	snapshotRoundTrip(t, cfg, s, at)
	cfg.BatchMaxSize = 8
	if s.BatchSize(cfg) != 2 {
		t.Fatal("estimate not used")
	}
	s.pending = []pendingGroup{{prs: []BatchPR{{PR: 223, HeadSHA: "h223"}, {PR: 224, HeadSHA: "h224"}, {PR: 225, HeadSHA: "h225"}}, bisection: true}}
	if prs, _ := s.batchCandidates(cfg, at); len(prs) != 3 {
		t.Fatal("adaptation resized pending half")
	}
	same := Apply(s, cfg.Applied("same", cfg.ConfigDigest))
	if f, n := same.FailureEstimate(); f != failures || n != 200 {
		t.Fatal("same digest reset history")
	}
	cfg.ConfigDigest = strings.Repeat("b", 64)
	s = Apply(s, cfg.Applied("changed", cfg.ConfigDigest))
	if _, n := s.FailureEstimate(); n != 0 || s.BatchSize(cfg) != 8 {
		t.Fatal("new digest did not restart history")
	}
}
func TestAdaptiveSnapshotValidationAndFixedPoint(t *testing.T) {
	cfg, s, at := adaptiveState(t, 4, 2)
	s, b := admitChain(t, cfg, s, at)
	s = confirmOriginal(s, b, ChecksFailed)
	s = Apply(s, Dequeued{PR: 1, AdmissionID: s.entry(1).AdmissionID, Reason: ChecksFailed})
	snapshotRoundTrip(t, cfg, s, at)
	for _, mutate := range []func(*stateSnapshot){
		func(v *stateSnapshot) { v.Window = nil },
		func(v *stateSnapshot) { v.Window.LastHalved.Reason = Conflict },
		func(v *stateSnapshot) { v.Window.Current = 0 }, func(v *stateSnapshot) { v.Window.Bounds.MinDepth = 5 },
		func(v *stateSnapshot) { v.Window.LastHalved.Revision = v.Revision + 1 },
		func(v *stateSnapshot) { v.Outcomes = append(v.Outcomes, v.Outcomes[0]) },
		func(v *stateSnapshot) { v.Outcomes[0].AdmissionID = v.NextAdmission + 1 },
		func(v *stateSnapshot) { v.Outcomes[0].Revision = v.Revision + 1 },
		func(v *stateSnapshot) { v.Outcomes = make([]changeOutcome, 201) },
	} {
		raw, _ := EncodeSnapshot(s)
		var v stateSnapshot
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		mutate(&v)
		raw, _ = json.Marshal(v)
		if _, err := DecodeSnapshot(raw); err == nil {
			t.Fatal("accepted invalid adaptive snapshot")
		}
	}
}
func TestAdaptiveConfigAppliedGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/adaptive/config_applied.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := DecodeEvent(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, encoded, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), encoded) {
		t.Fatal("adaptive config wire changed")
	}
	for _, invalid := range []string{`null`, `{}`, `{"min_depth":1}`, `{"min_depth":0,"max_depth":4}`, `{"min_depth":3,"max_depth":2}`, `{"min_depth":1,"max_depth":33}`} {
		malformed := bytes.Replace(encoded, []byte(`{"min_depth":1,"max_depth":4}`), []byte(invalid), 1)
		if _, err := DecodeEvent(malformed); err == nil {
			t.Fatalf("accepted bounds %s", invalid)
		}
	}
}

func TestAdaptiveUpgradeClampsExistingSerialQueueAndInitializesOldSpeculation(t *testing.T) {
	cfg, s, _ := chainState(t, 1, 3)
	cfg.ConfigDigest = strings.Repeat("a", 64)
	s = Apply(s, cfg.Applied("serial", cfg.ConfigDigest))
	cfg.ConfigDigest = strings.Repeat("b", 64)
	cfg.SpeculationMaxDepth = 4
	s = Apply(s, cfg.Applied("speculation", cfg.ConfigDigest))
	if s.Window(cfg) != 1 {
		t.Fatal("enabling speculation reset existing serial W")
	}
	oldCfg, old, at := chainState(t, 4, 3)
	oldCfg.ConfigDigest = strings.Repeat("c", 64)
	old = Apply(old, ConfigApplied{Commit: "old-binary", Digest: oldCfg.ConfigDigest})
	old, _ = admitChain(t, oldCfg, old, at)
	if old.WindowInitialized() || old.Window(oldCfg) != 4 {
		t.Fatal("legacy speculative log lost fixed admission")
	}
	migrated := Apply(old, oldCfg.Applied("current", oldCfg.ConfigDigest))
	if migrated.Window(oldCfg) != 4 || !migrated.WindowInitialized() || len(migrated.Batches()) != 1 {
		t.Fatal("bounds initialization discarded admitted work")
	}
	snapshotRoundTrip(t, oldCfg, migrated, at)
}

func TestOldIdlePolicyInitializesBoundsAtItsFixedCeiling(t *testing.T) {
	cfg := Config{SpeculationMaxDepth: 4, ConfigDigest: strings.Repeat("a", 64)}
	s := Apply(State{}, ConfigApplied{Commit: "old-policy", Digest: cfg.ConfigDigest})
	// No chained batch need have started since the last old-binary policy event.
	if s.WindowInitialized() {
		t.Fatal("old policy claimed to record bounds")
	}
	s = Apply(s, cfg.Applied("new-binary", cfg.ConfigDigest))
	if s.Window(cfg) != 4 || !s.WindowInitialized() {
		t.Fatal("old idle policy initialized below its fixed ceiling")
	}
}

func TestAdaptiveWindowBuildConflictsAreNeutral(t *testing.T) {
	for _, provisional := range []bool{false, true} {
		t.Run(fmt.Sprint("provisional=", provisional), func(t *testing.T) {
			cfg, s, at := adaptiveState(t, 8, 4)
			var head *Batch
			if provisional {
				s, head = admitChain(t, cfg, s, at)
			}
			var build BuildChainedTestRef
			for _, action := range Plan(cfg, s, at) {
				if a, ok := action.(BuildChainedTestRef); ok {
					build = a
				}
			}
			if build.BatchID == "" {
				t.Fatal("no original build")
			}
			s = Apply(s, ChainedBatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, ParentBatchID: build.ParentBatchID, Position: build.Position, At: at})
			b := s.Batch(build.BatchID)
			s = Apply(s, TestRefBuildFailed{BatchID: b.ID, Conflict: true, At: at})
			if provisional {
				s = settleChain(s, head, at)
				s = Apply(s, BatchDiagnosed{BatchID: b.ID, Reason: Conflict})
			}
			if s.Window(cfg) != 8 || s.LastWindowFailure() != nil {
				t.Fatal("build conflict halved W")
			}
			if f, n := s.FailureEstimate(); f != 0 || n != map[bool]int{false: 0, true: 1}[provisional] {
				t.Fatalf("conflict entered estimate: %d/%d", f, n)
			}
			snapshotRoundTrip(t, cfg, s, at)
		})
	}
}
func TestAdaptiveWindowFailureAtInitialFloorHasNoHalvingEvidence(t *testing.T) {
	cfg, s, at := adaptiveState(t, 4, 1)
	cfg.SpeculationMinDepth = 4
	cfg.ConfigDigest = strings.Repeat("b", 64)
	s = Apply(s, cfg.Applied("floor", cfg.ConfigDigest))
	s, b := admitChain(t, cfg, s, at)
	s = confirmOriginal(s, b, ChecksTimeout)
	if s.Window(cfg) != 4 || s.LastWindowFailure() != nil {
		t.Fatal("failure at floor recorded a halving")
	}
}
