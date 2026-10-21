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

func checkFixture(t *testing.T) LandingChecksRecorded {
	t.Helper()
	raw, err := os.ReadFile("testdata/wire/v1/event_landing_checks_recorded.json")
	if err != nil {
		t.Fatal(err)
	}
	e, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e.(LandingChecksRecorded)
}

func TestLandingChecksStrictWire(t *testing.T) {
	e := checkFixture(t)
	_, raw, err := EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"missing": func(b []byte) []byte { return bytes.Replace(b, []byte(`"attempt":2,`), nil, 1) },
		"unknown": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"result":"success"`), []byte(`"result":"success","extra":true`), 1)
		},
		"case": func(b []byte) []byte { return bytes.Replace(b, []byte(`"run_id"`), []byte(`"Run_ID"`), 1) },
		"duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"run_id":701`), []byte(`"run_id":701,"run_id":702`), 1)
		},
		"zero_attempt": func(b []byte) []byte { return bytes.Replace(b, []byte(`"attempt":2`), []byte(`"attempt":0`), 1) },
		"null_result": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"result":"success"`), []byte(`"result":null`), 1)
		},
		"missing_input": func(b []byte) []byte { return bytes.Replace(b, []byte(`"unsettled":[]`), []byte(`"ignored":[]`), 1) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEvent(mutate(raw)); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	// Explicit unavailability survives roundtrip without a fabricated zero ID.
	e.Checks[0].RunID = nil
	e.Checks[0].Attempt = nil
	e.Checks[0].WorkflowRunID = nil
	e.Checks[0].ProducerKind = "unavailable"
	e.Checks[0].ProducerIdentity = nil
	e.Checks[0].Source = ""
	_, raw, err = EncodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatal("unavailable evidence lost", err)
	}
}

func TestLandingChecksJournalOnlyAndSnapshotGate(t *testing.T) {
	e := checkFixture(t)
	for _, authority := range []bool{false, true} {
		s := State{BaseBranch: "main", BaseSHA: strings.Repeat("a", 40)}
		if authority {
			pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
			s = Apply(s, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
			s = Apply(s, pin)
		}
		before, err := EncodeSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		next := Apply(s, e)
		after, err := EncodeSnapshot(next)
		if err != nil || !bytes.Equal(before, after) || !reflect.DeepEqual(s, next) {
			t.Fatal("journal evidence changed planner state", err)
		}
		var envelope struct{ Version int }
		if err := json.Unmarshal(after, &envelope); err != nil {
			t.Fatal(err)
		}
		want := 6
		if authority {
			want = 7
		}
		if envelope.Version != want {
			t.Fatalf("snapshot version %d want %d", envelope.Version, want)
		}
		restored, err := DecodeSnapshot(before)
		if err != nil {
			t.Fatal(err)
		}
		tail, err := EncodeSnapshot(Apply(restored, e))
		if err != nil || !bytes.Equal(tail, after) || !reflect.DeepEqual(Plan(Config{}, restored, time.Time{}), Plan(Config{}, next, time.Time{})) {
			t.Fatal("snapshot tail differs")
		}
	}
}
