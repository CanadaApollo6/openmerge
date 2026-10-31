package queue

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Retain the review probe: compare full replay with a snapshot at every prefix.
func TestReviewPR46TransitionReplay(t *testing.T) {
	at := time.Unix(1, 0).UTC()
	cfg := Config{MergeMethod: Merge, SpeculationMaxDepth: 8, BatchMaxSize: 2, CheckTimeout: time.Hour, ConfigDigest: strings.Repeat("a", 64)}
	seed := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	state := seed
	var log []Event
	var snapshots [][]byte
	record := func(e Event) {
		old, _ := EncodeSnapshot(state)
		next := Apply(state, e)
		unchanged, _ := EncodeSnapshot(state)
		if !bytes.Equal(old, unchanged) {
			t.Fatalf("Apply mutated input for %T", e)
		}
		state = next
		log = append(log, e)
		raw, err := EncodeSnapshot(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeSnapshot(raw); err != nil {
			t.Fatalf("snapshot after %T: %v", e, err)
		}
		snapshots = append(snapshots, raw)
	}
	wantW := func(w int) {
		if state.Window(cfg) != w {
			t.Fatalf("event %d W=%d want=%d", len(log), state.Window(cfg), w)
		}
	}
	record(cfg.Applied("p1", cfg.ConfigDigest))
	for pr := 1; pr <= 14; pr++ {
		record(PRObserved{PR: pr, HeadSHA: fmt.Sprint("h", pr), Eligible: true, Trusted: true})
		record(EnqueueRequested{PR: pr, At: at})
	}
	admit := func() *Batch {
		for _, action := range Plan(cfg, state, at) {
			if a, ok := action.(BuildChainedTestRef); ok {
				e := ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: at, ParentBatchID: a.ParentBatchID, Position: a.Position}
				if !CanStart(cfg, state, a, e) {
					t.Fatal("planned admission rejected")
				}
				record(e)
				record(ChainedTestRefBuilt{BatchID: a.BatchID, TestedSHA: "t-" + a.BatchID, LandingSHA: "t-" + a.BatchID, Method: Merge, At: at, ParentBatchID: a.ParentBatchID, Position: a.Position})
				return state.Batch(a.BatchID)
			}
		}
		t.Fatal("no admission")
		return nil
	}
	land := func(b *Batch) {
		record(ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
		var chosen Land
		for _, a := range Plan(cfg, state, at) {
			if a, ok := a.(Land); ok && a.BatchID == b.ID {
				chosen = a
			}
		}
		if chosen.BatchID == "" {
			t.Fatalf("no safe landing for %s", b.ID)
		}
		record(LandingStarted{BatchID: b.ID, AttemptID: "attempt-" + b.ID, FenceToken: state.Fence, At: at})
		record(Landed{BatchID: b.ID, NewBaseSHA: b.TestedSHA, NewFence: "f-" + b.ID})
	}
	confirm := func(b *Batch, reason DequeueReason) {
		b = state.Batch(b.ID)
		record(ChecksFailureConfirmed{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Reason: reason})
	}
	first, second, third := admit(), admit(), admit()
	record(ChecksCompleted{TestedSHA: second.TestedSHA, Success: false})
	wantW(8)
	confirm(second, ChecksFailed)
	wantW(8) // suffix cannot confirm
	land(first)
	wantW(8)
	// The promoted original had been admitted at W=8.
	confirm(second, ChecksFailed)
	wantW(4)
	if state.Batch(third.ID).InvalidationReason == "" {
		t.Fatal("failed prefix left live successor")
	}
	last := state.LastWindowFailure()
	for _, e := range []Event{ChecksCompleted{TestedSHA: third.TestedSHA, Success: false}, BatchFailed{BatchID: third.ID, Reason: ChecksTimeout}, ChecksFailureConfirmed{BatchID: third.ID, TestedSHA: third.TestedSHA, Reason: ChecksTimeout}, TestRefBuildFailed{BatchID: third.ID, Conflict: true, At: at}} {
		record(e)
		wantW(4)
	}
	inv, _ := state.pendingInvalidation()
	record(BatchesInvalidated(inv))
	record(ChecksCompleted{TestedSHA: third.TestedSHA, Success: false})
	wantW(4)
	if !reflect.DeepEqual(last, state.LastWindowFailure()) {
		t.Fatal("late failure replaced accepted evidence")
	}
	half := admit()
	if !half.FromBisection {
		t.Fatal("lost lineage")
	}
	confirm(half, ChecksTimeout)
	wantW(4)
	confirm(half, ChecksFailed)
	wantW(4)
	d := state.dequeues[half.PRs[0].PR]
	record(Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason, BatchID: d.BatchID})
	wantW(4)
	record(Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason, BatchID: d.BatchID})
	wantW(4)
	other := admit()
	if !other.FromBisection {
		t.Fatal("lost second half")
	}
	land(other)
	wantW(5)
	record(Landed{BatchID: other.ID, NewBaseSHA: other.TestedSHA, NewFence: "duplicate"})
	wantW(5)
	f, n := state.FailureEstimate()
	if f != 1 || n != 4 {
		t.Fatalf("estimate %d/%d want 1/4", f, n)
	}
	cfg.SpeculationMinDepth = 6
	cfg.ConfigDigest = strings.Repeat("b", 64)
	record(cfg.Applied("p2", cfg.ConfigDigest))
	wantW(6)
	if _, n := state.FailureEstimate(); n != 0 {
		t.Fatal("bounds digest did not reset history")
	}
	cfg.SpeculationMinDepth = 1
	cfg.SpeculationMaxDepth = 3
	cfg.ConfigDigest = strings.Repeat("c", 64)
	record(cfg.Applied("p3", cfg.ConfigDigest))
	wantW(3)
	expected, _ := EncodeSnapshot(state)
	replay := seed
	for _, e := range log {
		replay = Apply(replay, e)
	}
	raw, _ := EncodeSnapshot(replay)
	if !bytes.Equal(raw, expected) {
		t.Fatal("full replay diverged")
	}
	for cut, snapshot := range snapshots {
		restored, err := DecodeSnapshot(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range log[cut+1:] {
			restored = Apply(restored, e)
		}
		raw, _ := EncodeSnapshot(restored)
		if !bytes.Equal(raw, expected) || !reflect.DeepEqual(Plan(cfg, restored, at), Plan(cfg, state, at)) {
			t.Fatalf("snapshot+tail diverged at cut %d", cut)
		}
	}
	t.Logf("%d events; every snapshot cut equals full fold; final W=%d", len(log), state.Window(cfg))
}

// Reconciliation records the historical fixed policy before a replacement.
func TestReviewPR46LegacyClamp(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint("changed=", changed), func(t *testing.T) {
			old, s, at := chainState(t, 8, 2)
			old.ConfigDigest = strings.Repeat("a", 64)
			s = Apply(s, ConfigApplied{Commit: "old-binary", Digest: old.ConfigDigest})
			s, b := admitChain(t, old, s, at)
			s = settleChain(s, b, at)
			if s.Window(old) != 8 {
				t.Fatal("old fixed window missing")
			}
			s = Apply(s, old.Applied("old-binary", old.ConfigDigest))
			s = snapshotRoundTrip(t, old, s, at)
			next := old
			want := 8
			if changed {
				next.SpeculationMaxDepth = 4
				next.ConfigDigest = strings.Repeat("b", 64)
				want = 4
			}
			s = Apply(s, next.Applied("changed-at-upgrade", next.ConfigDigest))
			if s.Window(next) != want || !s.WindowInitialized() {
				t.Fatalf("legacy W=8, new max=%d: got W=%d, want %d", next.MaxDepth(), s.Window(next), want)
			}
		})
	}
}

func TestReviewPR46FreshSizeRevalidatesQueuedAdmission(t *testing.T) {
	cfg, s, at := adaptiveState(t, 4, 0)
	cfg.BatchMaxSize = 8
	cfg.BatchTargetPassRate = .8
	// This pins the adaptive cap itself; the guarded case is checked below and
	// in backlog_guard_test.go.
	cfg.BatchBacklogGuardOff = true
	enqueue := func(pr int) {
		s = Apply(s, PRObserved{PR: pr, HeadSHA: fmt.Sprint("h", pr), Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: pr, At: at})
	}
	for pr := 1; pr <= 19; pr++ {
		enqueue(pr)
		var b *Batch
		s, b = admitChain(t, cfg, s, at)
		if pr <= 2 {
			s = confirmOriginal(s, b, ChecksFailed)
			d := s.dequeues[pr]
			s = Apply(s, Dequeued{PR: pr, AdmissionID: d.AdmissionID, Reason: ChecksFailed})
		} else {
			s = settleChain(s, b, at)
		}
	}
	enqueue(20)
	s, head := admitChain(t, cfg, s, at)
	enqueue(21)
	s, _ = admitChain(t, cfg, s, at)
	for pr := 22; pr <= 30; pr++ {
		enqueue(pr)
	}
	var pending BuildChainedTestRef
	for _, a := range Plan(cfg, s, at) {
		if a, ok := a.(BuildChainedTestRef); ok {
			pending = a
		}
	}
	if len(pending.PRs) != 8 {
		t.Fatal("expected cold-start 8-member admission")
	}
	s = settleChain(s, head, at)
	if s.BatchSize(cfg) != 2 {
		t.Fatalf("20-sample size=%d want=2", s.BatchSize(cfg))
	}
	var replacement BuildChainedTestRef
	for _, a := range Plan(cfg, s, at) {
		if a, ok := a.(BuildChainedTestRef); ok {
			replacement = a
		}
	}
	if len(replacement.PRs) != 2 {
		t.Fatalf("replacement cap=%d want=2", len(replacement.PRs))
	}
	t.Logf("old key=%s; new key=%s", pending.Key(), replacement.Key())
	start := ChainedBatchStarted{BatchID: pending.BatchID, BaseSHA: pending.BaseSHA, PRs: pending.PRs, ParentBatchID: pending.ParentBatchID, Position: pending.Position, At: at}
	if CanStart(cfg, s, pending, start) {
		t.Fatal("stale size-8 admission passed start gate after size shrank to 2")
	}
	t.Log("stale size-8 build rejected after 20th sample reduces fresh cap to 2")
	// With the backlog guard, nine waiting changes over a window of four floor
	// the size at 3: still below the stale 8, so the start is still rejected.
	guarded := cfg
	guarded.BatchBacklogGuardOff = false
	if got := len(freshBatch(guarded, s, at)); got != 3 {
		t.Fatalf("guarded replacement cap=%d want=3", got)
	}
	if CanStart(guarded, s, pending, start) {
		t.Fatal("stale size-8 admission passed the guarded start gate")
	}
}
