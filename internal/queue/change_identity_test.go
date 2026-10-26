package queue

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func changeIdentity(t *testing.T) ChangeID {
	t.Helper()
	id, err := PRChangeID("7", "Owner/Repo", "main", "", 12)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func changeObservation(t *testing.T, revision ChangeRevision, head string) ChangeRevisionObserved {
	t.Helper()
	return ChangeRevisionObserved{PR: 12, ChangeID: changeIdentity(t), Revision: revision, Predecessor: revision - 1, HeadSHA: head,
		Basis: RevisionObservationBasis{BaseSHA: "base-1", At: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}}
}

func TestChangeIdentityVectors(t *testing.T) {
	for _, tc := range []struct {
		installation, repository, base, lane string
		pr                                   int
		want                                 ChangeID
	}{
		{"7", "Owner/Repo", "main", "", 12, "chg1_b118ba35bcf7a6892c3ae1c61a197f6b3e89e8f3c762b74f22e5f04fcbe04b46"},
		{"8", "Owner/Repo", "main", "", 12, "chg1_2269d7de8353209d0c7ea6e54885a849b66edfc5ade6c06d0e8e089f9a67a376"},
		{"7", "Owner/Repo", "release/1", "", 12, "chg1_bfbe52ddf86488a62f14120271eec03d75db108ea6742a4385ff746326881650"},
		{"7", "Owner/Repo", "main", "batch", 12, "chg1_e5e1ea1dc9396370aaf5ce9b7cc006e4a20dc6702ba6a46561a896819e7860a3"},
		{"7", "Owner/Repo", "main", "", 13, "chg1_fcf1d60ffe35af5ddf005d6eb811bdbf0eb0807de073e882fd8ffccbe8e6fa2a"},
		{"7", "owner/repo", "main", "", 12, "chg1_f7e9038d6460b3246f42e86785ba5684bc2294763fe8cd8d21999765464034f1"},
	} {
		got, err := PRChangeID(tc.installation, tc.repository, tc.base, tc.lane, tc.pr)
		if err != nil || got != tc.want {
			t.Fatalf("identity %+v = %s, %v", tc, got, err)
		}
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	// A delimiter in one component cannot impersonate a component boundary.
	a, _ := PRChangeID("a/b", "c", "main", "", 12)
	b, _ := PRChangeID("a", "b/c", "main", "", 12)
	if a == b {
		t.Fatal("ambiguous queue key encoding")
	}
	for _, args := range [][3]string{{"", "repo", "main"}, {"7", "", "main"}, {"7", "repo", ""}} {
		if _, err := PRChangeID(args[0], args[1], args[2], "", 12); err == nil {
			t.Fatal("accepted empty identity component")
		}
	}
	if _, err := PRChangeID("7", "repo", "main", "", 0); err == nil {
		t.Fatal("accepted invalid PR")
	}
}

func TestChangeRevisionWireValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire/v1/event_change_revision_observed.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(event, changeObservation(t, 1, "head-1")) {
		t.Fatalf("decode %v %v", event, err)
	}
	_, encoded, err := EncodeEvent(event)
	if err != nil || !bytes.Equal(encoded, raw) {
		t.Fatalf("round trip %s %v", encoded, err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		t.Run("missing-"+key, func(t *testing.T) {
			copyFields := make(map[string]json.RawMessage)
			for k, v := range fields {
				if k != key {
					copyFields[k] = v
				}
			}
			data, _ := json.Marshal(copyFields)
			envelope["data"] = data
			invalid, _ := json.Marshal(envelope)
			if _, err := DecodeEvent(invalid); err == nil {
				t.Fatalf("accepted missing %s", key)
			}
		})
	}
	for _, invalid := range [][]byte{
		bytes.Replace(raw, []byte(`"pr":12`), []byte(`"pr":0`), 1),
		bytes.Replace(raw, []byte(`"revision":1`), []byte(`"revision":0`), 1),
		bytes.Replace(raw, []byte(`"predecessor":0`), []byte(`"predecessor":1`), 1),
		bytes.Replace(raw, []byte(`"head_sha":"head-1"`), []byte(`"head_sha":""`), 1),
		bytes.Replace(raw, []byte(`"base_sha":"base-1"`), []byte(`"base_sha":""`), 1),
		bytes.Replace(raw, []byte(`"chg1_`), []byte(`"chg2_`), 1),
		bytes.Replace(raw, []byte(`"base_sha":"base-1",`), nil, 1),
		bytes.Replace(raw, []byte(`"at":"2026-10-17T12:00:00Z"`), []byte(`"at":null`), 1),
		bytes.Replace(raw, []byte(`"at":"2026-10-17T12:00:00Z"`), []byte(`"at":"0001-01-01T00:00:00Z"`), 1),
		bytes.Replace(raw, []byte(`"pr":12`), []byte(`"extra":true,"pr":12`), 1),
		bytes.Replace(raw, []byte(`"base_sha":`), []byte(`"extra":true,"base_sha":`), 1),
	} {
		if _, err := DecodeEvent(invalid); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
	invalid := changeObservation(t, 1, "head-1")
	invalid.Predecessor = 2
	if _, _, err := EncodeEvent(invalid); err == nil {
		t.Fatal("encoded invalid revision")
	}
}

func TestLegacyChangeRevisionsReplayAndReenqueue(t *testing.T) {
	events := []Event{
		BaseMoved{SHA: "base-1"}, PRObserved{PR: 12, HeadSHA: "H1", Eligible: true, Trusted: true}, EnqueueRequested{PR: 12}, EnqueueRequested{PR: 12},
		PRObserved{PR: 12, HeadSHA: "H1", Eligible: true, Trusted: true}, DequeueRequested{12}, Dequeued{PR: 12, AdmissionID: 1, Reason: Requested},
		EnqueueRequested{PR: 12}, PRObserved{PR: 12, HeadSHA: "H2", Eligible: true, Trusted: true}, PRClosed{12},
		PRObserved{PR: 12, HeadSHA: "H1", Eligible: true, Trusted: true}, EnqueueRequested{PR: 12},
	}
	var first, replay State
	for i, event := range events {
		previous := first
		first = Apply(first, event)
		if i == 1 {
			if _, ok := previous.PRRevision(12); ok {
				t.Fatal("Apply mutated previous revision map")
			}
		}
		_, raw, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		replay = Apply(replay, restored)
		if !reflect.DeepEqual(first.changeRevisions, replay.changeRevisions) {
			t.Fatalf("replay differs at %d", i)
		}
	}
	current, ok := first.PRRevision(12)
	if !ok || current.Revision != 3 || current.HeadSHA != "H1" || current.ChangeID != "" || !current.Basis.At.IsZero() {
		t.Fatalf("revision sequence %v", current)
	}
	closed := Apply(first, PRClosed{12})
	retained, _ := closed.PRRevision(12)
	if !reflect.DeepEqual(retained, current) {
		t.Fatal("closure erased durable revision boundary")
	}
}

func TestChangeRevisionPredecessorsAndObservationOrder(t *testing.T) {
	for _, legacyFirst := range []bool{true, false} {
		var state State
		for i, head := range []string{"H1", "H2", "H1"} {
			e := changeObservation(t, ChangeRevision(i+1), head)
			if legacyFirst {
				state = Apply(state, PRObserved{PR: 12, HeadSHA: head, Eligible: true, Trusted: true})
			}
			before := state
			if !state.CanObserveChangeRevision(e) {
				t.Fatalf("refused observation %v legacyFirst=%v", e, legacyFirst)
			}
			state = Apply(state, e)
			if reflect.DeepEqual(before.changeRevisions, state.changeRevisions) {
				t.Fatal("missing revision evidence")
			}
			if !legacyFirst {
				state = Apply(state, PRObserved{PR: 12, HeadSHA: head, Eligible: true, Trusted: true})
			}
			current, _ := state.PRRevision(12)
			if current.Revision != e.Revision || current.Basis != e.Basis {
				t.Fatalf("double-counted revision %v", current)
			}
			for _, stale := range []ChangeRevisionObserved{e, changeObservation(t, 1, "old"), changeObservation(t, ChangeRevision(i+3), "future")} {
				rejected := Apply(state, stale)
				if !reflect.DeepEqual(state, rejected) {
					t.Fatal("stale update changed state")
				}
			}
		}
	}
}

func TestChangeRevisionKeepsHeadChangedAndLandingBinding(t *testing.T) {
	cfg, state, now := chainState(t, 4, 1)
	state, batch := admitChain(t, cfg, state, now)
	original := state.BatchRevisions(batch.ID)
	state = Apply(state, ChecksCompleted{TestedSHA: batch.TestedSHA, Success: true})
	state = Apply(state, LandingStarted{BatchID: batch.ID, AttemptID: "attempt", FenceToken: state.Fence, At: now})
	id, err := PRChangeID("7", "Owner/Repo", "main", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	state = Apply(state, ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 2, Predecessor: 1, HeadSHA: "H2", Basis: RevisionObservationBasis{BaseSHA: "base", At: now}})
	state = Apply(state, PRObserved{PR: 1, HeadSHA: "H2", Eligible: true, Trusted: true})
	if state.Active.Phase != Landing || state.Active.PRs[0].HeadSHA != original[0].HeadSHA || !reflect.DeepEqual(state.BatchRevisions(batch.ID), original) {
		t.Fatal("new revision rebound started landing")
	}
	current, _ := state.PRRevision(1)
	if current.Revision != 2 {
		t.Fatal("new head not projected")
	}
	original[0].Revision = 999
	if state.BatchRevisions(batch.ID)[0].Revision != 1 {
		t.Fatal("caller mutated binding")
	}
	state = Apply(state, Landed{BatchID: batch.ID, NewBaseSHA: batch.TestedSHA, NewFence: "next"})
	if len(state.Entries) != 0 || len(state.batchRevisions) != 0 {
		t.Fatal("landing did not settle and retire bounded batch state")
	}
	current, _ = state.PRRevision(1)
	if current.HeadSHA != "H2" {
		t.Fatal("H1 settlement changed H2 projection")
	}

	var waiting State
	waiting = Apply(waiting, PRObserved{PR: 12, HeadSHA: "H1", Eligible: true, Trusted: true})
	waiting = Apply(waiting, EnqueueRequested{PR: 12})
	waiting = Apply(waiting, changeObservation(t, 2, "H2"))
	if waiting.dequeues[12].Reason != HeadChanged {
		t.Fatal("new observation bypassed HeadChanged")
	}
}
