package queue

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOutsidePushAcknowledgementWireValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire/v1/event_outside_push_acknowledged.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"source_event_id", "sha", "operator", "at"} {
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(raw, &envelope)
		var data map[string]json.RawMessage
		_ = json.Unmarshal(envelope["data"], &data)
		delete(data, field)
		envelope["data"], _ = json.Marshal(data)
		missing, _ := json.Marshal(envelope)
		if _, err := DecodeEvent(missing); err == nil {
			t.Errorf("accepted missing %s", field)
		}
	}
	for _, data := range []string{
		strings.Replace(string(raw), `"operator":"On-call operator"`, `"operator":" "`, 1),
		strings.Replace(string(raw), `"source_event_id":42`, `"source_event_id":0`, 1),
		strings.Replace(string(raw), `"sha":"outside"`, `"sha":""`, 1),
		strings.Replace(string(raw), `"at":"2026-10-20T12:00:00Z"`, `"at":"0001-01-01T00:00:00Z"`, 1),
		strings.Replace(string(raw), `"operator":`, `"Operator":`, 1),
		strings.Replace(string(raw), `"operator":`, `"unknown":true,"operator":`, 1),
		strings.Replace(string(raw), `"source_event_id":42`, `"source_event_id":42,"source_event_id":43`, 1),
	} {
		if _, err := DecodeEvent([]byte(data)); err == nil {
			t.Errorf("accepted %s", data)
		}
	}
	valid := e.(OutsidePushAcknowledged)
	for _, mutate := range []func(*OutsidePushAcknowledged){
		func(e *OutsidePushAcknowledged) { e.Operator = strings.Repeat("x", 257) },
		func(e *OutsidePushAcknowledged) { e.Operator = "name\nforged" },
		func(e *OutsidePushAcknowledged) { e.SHA = "sha whitespace" },
	} {
		invalid := valid
		mutate(&invalid)
		if _, _, err := EncodeEvent(invalid); err == nil {
			t.Fatal("encoded invalid acknowledgement")
		}
	}
}

func TestOutsidePushAcknowledgementIsPlannerInert(t *testing.T) {
	// Include all independent legacy blocks plus real decision/approval evidence.
	var fixture struct {
		Events []json.RawMessage
		Config Config
	}
	raw, err := os.ReadFile("testdata/landing-start-activation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	check := func(s State) {
		t.Helper()
		before, err := EncodeSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		ack := OutsidePushAcknowledged{SourceEventID: 42, SHA: "outside", Operator: "on-call", At: epoch}
		next := Apply(s, ack)
		after, err := EncodeSnapshot(next)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) || !reflect.DeepEqual(s, next) || !reflect.DeepEqual(Plan(fixture.Config, s, epoch), Plan(fixture.Config, next, epoch)) {
			t.Fatal("acknowledgement changed state/actions")
		}
		// Snapshot-plus-tail and replay give exactly the same state and plans.
		snap, err := DecodeSnapshot(before)
		if err != nil {
			t.Fatal(err)
		}
		replay := Apply(snap, ack)
		got, _ := EncodeSnapshot(replay)
		if !bytes.Equal(before, got) || !reflect.DeepEqual(Plan(fixture.Config, s, epoch), Plan(fixture.Config, replay, epoch)) {
			t.Fatal("snapshot tail differs")
		}
	}
	for _, raw := range fixture.Events {
		e, err := DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		s = Apply(s, e)
		check(s)
	}
	for _, e := range []Event{RulesSourceBlocked{Source: Rulesets, Problems: []string{"rules"}}, RulesSourceBlocked{Source: Permissions, Problems: []string{"permissions"}}, RulesSourceBlocked{Source: ClassicProtection, Problems: []string{"classic"}}, BlockSourceBlocked{Source: LandingNotOnBase, Subject: "suspect", Detail: "suspect landing"}, PauseRequested{}, OutsidePushRecorded{PreviousSHA: "base", NewSHA: "outside", VerificationBeforeLanding: "none", PossibleStaleObservation: true, At: time.Now().UTC()}} {
		s = Apply(s, e)
		check(s)
	}
}
