package queue

import (
	"testing"
	"time"
)

func TestHeldRequestJournalAndSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	events := []Event{PRObserved{PR: 1, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}, EnqueueRequested{PR: 1, At: at}}
	var state State
	for _, event := range events {
		_, data, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		state = Apply(state, restored)
	}
	before := state
	state = snapshotRoundTrip(t, Config{}, state, at)
	state = Apply(state, PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true})
	if len(state.Entries) != 1 || state.Entries[0].HeadSHA != "head" || !state.Entries[0].EnqueuedAt.Equal(at) {
		t.Fatal("request did not survive journal/snapshot replay", state.Entries)
	}
	if _, held := state.HeldRequest(1); held {
		t.Fatal("admitted request still held")
	}
	if _, held := before.HeldRequest(1); !held || len(before.Entries) != 0 {
		t.Fatal("fold mutated earlier state")
	}
}

func TestHeldRequestCancelledByAuthorityRevision(t *testing.T) {
	state := Apply(State{}, PRObserved{PR: 12, HeadSHA: "H1", Trusted: true, PendingConditions: "sign-off"})
	state = Apply(state, EnqueueRequested{PR: 12})
	state = Apply(state, changeObservation(t, 2, "H2"))
	if _, held := state.HeldRequest(12); held {
		t.Fatal("new authority revision retained old-head request")
	}
	state = Apply(state, PRObserved{PR: 12, HeadSHA: "H2", Trusted: true, Eligible: true})
	if len(state.Entries) != 0 {
		t.Fatal("new authority head reused old request")
	}
}
