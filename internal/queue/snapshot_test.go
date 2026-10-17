package queue

import (
	"bytes"
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
	s = Apply(s, ChecksCompleted{TestedSHA: head.TestedSHA, Success: false})
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
