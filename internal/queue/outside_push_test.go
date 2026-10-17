package queue

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestOutsidePushReplaySnapshotAndPlannerIndependence(t *testing.T) {
	for _, initial := range []State{queued(1), testingState(), readyState(), landingState()} {
		before, err := EncodeSnapshot(initial)
		if err != nil {
			t.Fatal(err)
		}
		s, replay := initial, initial
		var events []Event
		for i := range OutsidePushLimit + 3 {
			event := OutsidePushRecorded{PreviousSHA: fmt.Sprintf("old%d", i), NewSHA: fmt.Sprintf("new%d", i), AncestryKnown: true, Rewrite: i%2 == 1, HistoryKnown: true, Commits: []string{fmt.Sprintf("new%d", i)}, VerificationBeforeLanding: "none", At: epoch}
			_, data, err := EncodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEvent(data)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, decoded)
			s = Apply(s, event)
		}
		for _, e := range events {
			replay = Apply(replay, e)
		}
		a, _ := EncodeSnapshot(s)
		b, _ := EncodeSnapshot(replay)
		if !bytes.Equal(a, b) {
			t.Fatal("replay not deterministic")
		}
		restored := snapshotRoundTrip(t, testConfig, s, epoch)
		if !reflect.DeepEqual(s.RecentOutsidePushes(), restored.RecentOutsidePushes()) || len(s.RecentOutsidePushes()) != OutsidePushLimit {
			t.Fatal("lost bounded history")
		}
		if !reflect.DeepEqual(Plan(testConfig, initial, epoch), Plan(testConfig, s, epoch)) || initial.revision != s.revision {
			t.Fatal("audit event changed planner behavior")
		}
		// Neither applying nor mutating a returned status list may modify an old state.
		s.RecentOutsidePushes()[0].Commits[0] = "mutated"
		after, _ := EncodeSnapshot(initial)
		if !bytes.Equal(before, after) || s.RecentOutsidePushes()[0].Commits[0] == "mutated" {
			t.Fatal("mutable history escaped")
		}
		// Ordinary BaseMoved keeps its exact prior invalidation/rebuild behavior.
		left, right := Apply(initial, BaseMoved{SHA: "next"}), Apply(s, BaseMoved{SHA: "next"})
		if !reflect.DeepEqual(Plan(testConfig, left, epoch), Plan(testConfig, right, epoch)) {
			t.Fatal("base invalidation changed")
		}
	}
}

func TestOutsidePushValidationAndUnknownRoundTrip(t *testing.T) {
	valid := OutsidePushRecorded{PreviousSHA: "old", NewSHA: "new", VerificationBeforeLanding: "none", At: epoch}
	for _, mutate := range []func(*OutsidePushRecorded){
		func(e *OutsidePushRecorded) { e.PreviousSHA = "" },
		func(e *OutsidePushRecorded) { e.NewSHA = e.PreviousSHA },
		func(e *OutsidePushRecorded) { e.Rewrite = true },
		func(e *OutsidePushRecorded) { e.Commits = []string{"new"} },
		func(e *OutsidePushRecorded) { e.Truncated = true },
		func(e *OutsidePushRecorded) { e.VerificationBeforeLanding = "passed" },
		func(e *OutsidePushRecorded) { e.HistoryKnown = true; e.Commits = []string{"new", "new"} },
		func(e *OutsidePushRecorded) {
			e.HistoryKnown = true
			e.Commits = make([]string, OutsidePushCommitLimit+1)
		},
	} {
		e := valid
		mutate(&e)
		if _, _, err := EncodeEvent(e); err == nil {
			t.Fatalf("accepted invalid evidence: %+v", e)
		}
	}
	_, data, err := EncodeEvent(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEvent(data)
	if err != nil || decoded.(OutsidePushRecorded).HistoryKnown {
		t.Fatalf("unknown became known: %s %v", data, err)
	}
	unknownState := Apply(State{}, valid)
	restored := snapshotRoundTrip(t, testConfig, unknownState, epoch)
	if len(restored.RecentOutsidePushes()) != 1 || restored.RecentOutsidePushes()[0].HistoryKnown {
		t.Fatal("snapshot lost unknown history")
	}
	// Strict required fields apply to the new type too.
	incomplete := bytes.Replace(data, []byte(`"rewrite":false,`), nil, 1)
	if _, err := DecodeEvent(incomplete); err == nil {
		t.Fatal("accepted missing rewrite flag")
	}
}
