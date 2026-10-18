package queue

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestChangeSnapshotTailEqualsFullReplay(t *testing.T) {
	cfg, state, now := chainState(t, 4, 2)
	state, batch := admitChain(t, cfg, state, now)
	id, err := PRChangeID("7", "Owner/Repo", "main", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := state.PRRevision(1)
	evidence := ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 1, HeadSHA: record.HeadSHA, Basis: RevisionObservationBasis{BaseSHA: state.BaseSHA, At: now}}
	events := []Event{
		evidence, ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true},
		LandingStarted{BatchID: batch.ID, AttemptID: "attempt", FenceToken: state.Fence, At: now},
		PRObserved{1, "H2", true, true}, PRClosed{1}, PRObserved{1, record.HeadSHA, true, true},
		Landed{BatchID: batch.ID, NewBaseSHA: batch.TestedSHA, NewFence: "next"}, EnqueueRequested{PR: 1, At: now.Add(time.Hour)},
	}
	for split := 0; split <= len(events); split++ {
		full := state
		for _, e := range events[:split] {
			full = Apply(full, e)
		}
		restored := snapshotRoundTrip(t, cfg, full, now)
		for _, e := range events[split:] {
			full = Apply(full, e)
			restored = Apply(restored, e)
			a, err := EncodeSnapshot(full)
			if err != nil {
				t.Fatal(err)
			}
			b, err := EncodeSnapshot(restored)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) || !reflect.DeepEqual(Plan(cfg, full, now), Plan(cfg, restored, now)) {
				t.Fatalf("snapshot tail differs at split %d", split)
			}
		}
		current, _ := restored.PRRevision(1)
		if current.Revision != 3 || current.HeadSHA != record.HeadSHA {
			t.Fatal("snapshot lost revision sequence")
		}
	}
}

func TestChangeSnapshotRejectsInvalidProjection(t *testing.T) {
	state := Apply(State{}, changeObservation(t, 1, "H1"))
	raw, err := EncodeSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(v map[string]any) { delete(v, "ChangeRevisions") },
		func(v map[string]any) { v["ChangeRevisions"] = map[string]any{"12": map[string]any{"Revision": 1}} },
		func(v map[string]any) { v["ChangeRevisions"].(map[string]any)["12"].(map[string]any)["Revision"] = 0 },
		func(v map[string]any) {
			v["ChangeRevisions"].(map[string]any)["12"].(map[string]any)["LogPosition"] = 99
		},
		func(v map[string]any) {
			v["ChangeRevisions"].(map[string]any)["12"].(map[string]any)["ChangeID"] = "bad"
		},
		func(v map[string]any) { v["BatchRevisions"] = map[string]any{"missing-batch": []any{}} },
	} {
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		change(value)
		invalid, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeSnapshot(invalid); err == nil {
			t.Fatalf("accepted invalid projection %s", invalid)
		}
	}
}

func TestChangeSnapshotDecodesVersion2WithoutInventingHistory(t *testing.T) {
	cfg, state, now := chainState(t, 4, 1)
	state, _ = admitChain(t, cfg, state, now)
	raw, err := EncodeSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Version"] = json.RawMessage(`2`)
	delete(fields, "Classifications")
	delete(fields, "ApprovalFacts")
	delete(fields, "ChangeRevisions")
	delete(fields, "BatchRevisions")
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, known := restored.PRRevision(1); known {
		t.Fatal("v2 invented historical revision sequence")
	}
	if !reflect.DeepEqual(Plan(cfg, state, now), Plan(cfg, restored, now)) {
		t.Fatal("v2 compatibility changed planner")
	}
	snapshotRoundTrip(t, cfg, restored, now)
	fields["ChangeRevisions"] = json.RawMessage(`{}`)
	invalid, _ := json.Marshal(fields)
	if _, err := DecodeSnapshot(invalid); err == nil {
		t.Fatal("accepted Stage A fields in version 2")
	}
}

func TestChangeSnapshotFastForwardPrefixRetainsBindings(t *testing.T) {
	cfg, state, now := chainState(t, 1, 2)
	cfg.BatchMaxSize = 2
	action := Plan(cfg, state, now)[0].(BuildTestRef)
	state = Apply(state, BatchStarted{BatchID: action.BatchID, BaseSHA: action.BaseSHA, PRs: action.PRs, At: now})
	batch := state.Active
	state = Apply(state, FastForwardBatchSelected{BatchID: batch.ID, PRs: batch.PRs[:1]})
	if len(state.BatchRevisions(batch.ID)) != 1 {
		t.Fatal("fast-forward prefix retained removed members")
	}
	snapshotRoundTrip(t, cfg, state, now)
}
