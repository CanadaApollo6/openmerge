package queue

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func snapshotRoundTrip(t *testing.T, cfg Config, s State, now time.Time) State {
	t.Helper()
	data, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Plan(cfg, s, now), Plan(cfg, restored, now)) {
		t.Fatalf("snapshot changed plan: %#v / %#v", Plan(cfg, s, now), Plan(cfg, restored, now))
	}
	if !reflect.DeepEqual(s.Batches(), restored.Batches()) || (len(s.pending) != 0 || len(restored.pending) != 0) && !reflect.DeepEqual(s.pending, restored.pending) {
		t.Fatalf("snapshot lost batch state")
	}
	return restored
}
func TestChainSnapshotPreservesRevocationRetriesAndPendingHalves(t *testing.T) {
	cfg, s, now := chainState(t, 4, 6)
	cfg.BatchMaxSize = 2
	s, head := admitChain(t, cfg, s, now)
	s, tail := admitChain(t, cfg, s, now)
	failed := ChecksObserved{BatchID: tail.ID, TestedSHA: tail.TestedSHA, Terminal: true, Timeout: cfg.CheckTimeout, OriginalDeadline: now.Add(cfg.CheckTimeout), Runs: []CheckRun{{Name: "ci", Source: "app:1", ID: 1, Attempt: 1, State: "failure"}}}
	s = Apply(s, failed)
	s = Apply(s, ChecksReopened{TestedSHA: tail.TestedSHA, At: now.Add(time.Minute)})
	s = snapshotRoundTrip(t, cfg, s, now.Add(time.Minute))
	s = Apply(s, ChecksFailureConfirmed{BatchID: head.ID, TestedSHA: head.TestedSHA, Generation: head.checksGeneration, Reason: ChecksFailed})
	s = snapshotRoundTrip(t, cfg, s, now)
	for _, a := range Plan(cfg, s, now) {
		if a, ok := a.(InvalidateBatches); ok {
			s = Apply(s, BatchesInvalidated(a))
			break
		}
	}
	s = snapshotRoundTrip(t, cfg, s, now)
	s, half := admitChain(t, cfg, s, now)
	if !half.FromBisection || len(half.PRs) != 1 {
		t.Fatal("lost halves")
	}
	s, _ = admitChain(t, cfg, s, now)
	snapshotRoundTrip(t, cfg, s, now)
}
func TestSnapshotPreservesStartedLandingAndEffects(t *testing.T) {
	cfg, s, now := chainState(t, 4, 3)
	s, b := admitChain(t, cfg, s, now)
	s, _ = admitChain(t, cfg, s, now)
	s = Apply(s, ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: b.ID, AttemptID: "attempt", FenceToken: s.Fence, At: now})
	s = Apply(s, LandingCleanupPolicyRecorded{BatchID: b.ID, AttemptID: "attempt", DeleteHeadBranch: true})
	restored := snapshotRoundTrip(t, cfg, s, now.Add(time.Minute))
	for _, e := range []Event{DequeueRequested{PR: b.PRs[0].PR}, HeadCleanupRequested{BatchID: b.ID, LandedSHA: b.TestedSHA}, Landed{BatchID: b.ID, NewBaseSHA: b.TestedSHA, NewFence: "next"}} {
		s, restored = Apply(s, e), Apply(restored, e)
		if !reflect.DeepEqual(Plan(cfg, s, now), Plan(cfg, restored, now)) {
			t.Fatal("snapshot continuation differs")
		}
	}
	snapshotRoundTrip(t, cfg, s, now)
}
func TestSnapshotRejectsMissingAndUnknownState(t *testing.T) {
	_, s, _ := chainState(t, 4, 1)
	data, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{[]byte(`{}`), bytes.Replace(data, []byte(`"Version":1`), []byte(`"Version":2`), 1), append(data, []byte(`{}`)...)} {
		if _, err := DecodeSnapshot(invalid); err == nil {
			t.Fatal("accepted malformed snapshot")
		}
	}
}

func TestSnapshotRejectsMissingNestedGeneration(t *testing.T) {
	cfg, s, now := chainState(t, 4, 1)
	s, _ = admitChain(t, cfg, s, now)
	data, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"ChecksGeneration":0,`), nil, 1)
	if _, err := DecodeSnapshot(data); err == nil {
		t.Fatal("snapshot silently manufactured a checks generation")
	}
}

func TestSnapshotContinuationBetweenSuffixInvalidations(t *testing.T) {
	cfg, s, now := chainState(t, 4, 3)
	s, first := admitChain(t, cfg, s, now)
	s, _ = admitChain(t, cfg, s, now)
	s, _ = admitChain(t, cfg, s, now)
	s = Apply(s, ChecksCompleted{TestedSHA: first.TestedSHA, Success: true})
	s = Apply(s, LandingStarted{BatchID: first.ID, AttemptID: "attempt", FenceToken: s.Fence, At: now})
	restored := snapshotRoundTrip(t, cfg, s, now)
	events := []Event{DequeueRequested{PR: 3}, BaseMoved{SHA: "outside"}, BatchesInvalidated{BatchIDs: []string{"b2"}, Reason: "base-moved"}, BatchesInvalidated{BatchIDs: []string{"b3"}, Reason: "dequeue"}}
	// Use the recorded reasons so each event follows the actual planner decision.
	for i, e := range events {
		if _, ok := e.(BatchesInvalidated); ok {
			a, pending := s.pendingInvalidation()
			if !pending {
				t.Fatal("no invalidation")
			}
			e = BatchesInvalidated(a)
		}
		s, restored = Apply(s, e), Apply(restored, e)
		restored = snapshotRoundTrip(t, cfg, restored, now)
		want, _ := EncodeSnapshot(s)
		got, _ := EncodeSnapshot(restored)
		if !bytes.Equal(want, got) {
			t.Fatalf("snapshot replay differs after event %d", i)
		}
	}
}

func TestSnapshotRejectsInvalidChainAndBounds(t *testing.T) {
	cfg, s, now := chainState(t, 4, 3)
	s, _ = admitChain(t, cfg, s, now)
	s, _ = admitChain(t, cfg, s, now)
	for _, tc := range []struct {
		name   string
		mutate func(*stateSnapshot)
	}{
		{"root parent", func(v *stateSnapshot) { v.Chain[0].Batch.ParentBatchID = "missing" }},
		{"root position", func(v *stateSnapshot) { v.Chain[0].Batch.ChainPosition = 0 }},
		{"root base", func(v *stateSnapshot) { v.Chain[0].Batch.BaseSHA = "wrong" }},
		{"duplicate PR", func(v *stateSnapshot) { v.Chain[1].Batch.PRs = v.Chain[0].Batch.PRs }},
		{"pending chain PR", func(v *stateSnapshot) { v.Pending = []groupSnapshot{{PRs: v.Chain[0].Batch.PRs}} }},
		{"duplicate pending PR", func(v *stateSnapshot) {
			v.Pending = []groupSnapshot{{PRs: []BatchPR{{PR: 3, HeadSHA: "h3"}}}, {PRs: []BatchPR{{PR: 3, HeadSHA: "h3"}}}}
		}},
		{"duplicate chain entry", func(v *stateSnapshot) { v.Entries = append(v.Entries, v.Entries[0]) }},
		{"duplicate waiting entry", func(v *stateSnapshot) { v.Entries = append(v.Entries, v.Entries[2]) }},
		{"unmarked gap", func(v *stateSnapshot) { v.Chain[1].Batch.ParentBatchID = "missing"; v.Chain[1].Batch.ChainPosition = 3 }},
		{"entries bound", func(v *stateSnapshot) { v.Entries = make([]Entry, 100001) }},
		{"effects bound", func(v *stateSnapshot) {
			v.Effects = make([]json.RawMessage, 100001)
			for i := range v.Effects {
				_, v.Effects[i], _ = EncodeAction(DeleteRef{Ref: "openmerge/queue/main/b1"})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := EncodeSnapshot(s)
			var v stateSnapshot
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&v)
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(data); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
}
