package authority_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func seeingEvidence(t *testing.T) (authority.Observation, string) {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Data struct {
			Evidence    authority.Observation `json:"evidence"`
			Fingerprint string                `json:"protection_fingerprint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Data.Evidence, fixture.Data.Fingerprint
}

func TestProtectionFingerprintCanonicalAndSensitive(t *testing.T) {
	o, want := seeingEvidence(t)
	if !o.SeeingComplete() {
		t.Fatal("fixture not a seeing read")
	}
	raw, _ := json.Marshal(o)
	reordered := authority.CloneObservation(o)
	reordered.Protections[0], reordered.Protections[1] = reordered.Protections[1], reordered.Protections[0]
	reordered.Coverage[0], reordered.Coverage[1] = reordered.Coverage[1], reordered.Coverage[0]
	for i := range reordered.Protections {
		p := &reordered.Protections[i]
		p.Rules[0], p.Rules[1] = p.Rules[1], p.Rules[0]
	}
	got, err := reordered.Fingerprint()
	if err != nil || got != want {
		t.Fatal("order changed fingerprint", got, want, err)
	}
	for _, change := range []func(*authority.Observation){
		func(o *authority.Observation) {
			o.Protections[0].ID = "another-id"
			o.Coverage[0].ProtectionIDs[0] = "repository_ruleset/another-id"
		},
		func(o *authority.Observation) { o.Protections[0].Enforcement = "disabled" },
		func(o *authority.Observation) { o.Protections[0].Includes[0] = "refs/heads/other" },
		func(o *authority.Observation) { o.Protections[0].Revision = "reliable-revision" },
		func(o *authority.Observation) {
			o.Protections[0].Conditions = []authority.Parameter{{Name: "repository_name", Values: []string{"other"}}}
		},
		func(o *authority.Observation) { o.Protections[0].Rules[1].Parameters[0].Values[0] = "true" },
		func(o *authority.Observation) { o.Protections[0].BypassActors[0].ID = "another-app" },
		func(o *authority.Observation) { o.Protections[0].BypassActors[0].Mode = "pull_request" },
	} {
		changed := authority.CloneObservation(o)
		change(&changed)
		got, err := changed.Fingerprint()
		if err != nil || got == want {
			t.Fatal("protection change reused fingerprint", err)
		}
	}
	after, _ := json.Marshal(o)
	if !reflect.DeepEqual(raw, after) {
		t.Fatal("fingerprint mutated evidence")
	}
	for _, visibility := range []authority.Visibility{authority.Hidden, authority.ReadFailure} {
		changed := authority.CloneObservation(o)
		changed.Protections[0].BypassVisibility = visibility
		changed.Protections[0].BypassActors = []authority.BypassActor{}
		if changed.SeeingComplete() {
			t.Fatal("unseen bypass list complete")
		}
		if _, err := changed.Fingerprint(); err == nil {
			t.Fatal("unseen list got fingerprint")
		}
	}
}

func TestObservationRejectsInvalidCombinations(t *testing.T) {
	o, _ := seeingEvidence(t)
	for _, change := range []func(*authority.Observation){
		func(o *authority.Observation) { o.Inventory = authority.Absent },
		func(o *authority.Observation) { o.Coverage[0].ProtectionIDs = []string{"phantom"} },
		func(o *authority.Observation) {
			o.Coverage[0].ProtectionIDs = append(o.Coverage[0].ProtectionIDs, o.Coverage[0].ProtectionIDs[0])
		},
		func(o *authority.Observation) { o.Protections[0].BypassVisibility = authority.Hidden },
		func(o *authority.Observation) { o.Protections[0].TargetsVisibility = authority.ReadFailure },
		func(o *authority.Observation) { o.Protections[0].BypassActors[0].Kind = "user" },
		func(o *authority.Observation) { o.Protections[0].Rules[0].Parameters = nil },
		func(o *authority.Observation) { o.Protections = append(o.Protections, o.Protections[0]) },
		func(o *authority.Observation) { o.Coverage[0].Visibility = authority.Absent },
		func(o *authority.Observation) {
			o.Findings = []authority.Finding{{Element: "bypass", Kind: "unexpected_bypass"}}
		},
	} {
		changed := authority.CloneObservation(o)
		change(&changed)
		if err := changed.Validate(); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
}
