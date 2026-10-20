package authority_test

import (
	"encoding/json"
	"fmt"
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
		func(o *authority.Observation) { o.Protections[0].Target = "tag" },
		func(o *authority.Observation) { o.Protections[0].Target = "push" },
		func(o *authority.Observation) { o.Protections[0].Target = "repository" },
		func(o *authority.Observation) { o.Protections[0].Includes[0] = "refs/heads/other" },
		func(o *authority.Observation) { o.Protections[0].Excludes = []string{"refs/heads/private/*"} },
		func(o *authority.Observation) { o.Protections[0].Revision = "reliable-revision" },
		func(o *authority.Observation) {
			o.Protections[0].Conditions = []authority.Parameter{{Name: "repository_name", Values: []string{"other"}}}
		},
		func(o *authority.Observation) { o.Protections[0].Rules[1].Parameters[0].Values[0] = "true" },
		func(o *authority.Observation) { o.Protections[0].Rules[0].Kind = "required_signatures" },
		func(o *authority.Observation) { o.Protections[0].BypassActors[0].Kind = "user" },
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

func TestUnknownBypassKindFingerprint(t *testing.T) {
	o, _ := seeingEvidence(t)
	o.Protections[0].BypassActors = []authority.BypassActor{
		{Kind: "unknown", RawKind: "FutureOne", Mode: "always"},
		{Kind: "unknown", RawKind: "FutureTwo", Mode: "always"},
	}
	want, err := o.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	reordered := authority.CloneObservation(o)
	reordered.Protections[0].BypassActors[0], reordered.Protections[0].BypassActors[1] = reordered.Protections[0].BypassActors[1], reordered.Protections[0].BypassActors[0]
	got, err := reordered.Fingerprint()
	if err != nil || got != want {
		t.Fatal("unknown actor order changed fingerprint", err)
	}
	o.Protections[0].BypassActors[0].RawKind = "AnotherFutureKind"
	got, err = o.Fingerprint()
	if err != nil || got == want {
		t.Fatal("unknown kind omitted from fingerprint", err)
	}
}

func TestUnseenProtectionTarget(t *testing.T) {
	o, _ := seeingEvidence(t)
	p := &o.Protections[0]
	p.Target = ""
	p.TargetsVisibility = authority.Hidden
	p.Includes, p.Excludes, p.Conditions = []string{}, []string{}, []authority.Parameter{}
	if err := o.Validate(); err != nil {
		t.Fatal("unseen target cannot be retained", err)
	}
	if _, err := o.Fingerprint(); err == nil {
		t.Fatal("unseen target received a fingerprint")
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
		func(o *authority.Observation) { o.Protections[0].BypassActors[0].Kind = "unknown" },
		func(o *authority.Observation) { o.Protections[0].Target = "" },
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

func TestBypassActorObservedKindsAndModes(t *testing.T) {
	for _, actor := range []authority.BypassActor{
		{Kind: "app", ID: "app"}, {Kind: "team", ID: "team"},
		{Kind: "repository_role", ID: "role"}, {Kind: "user", ID: "user"},
		{Kind: "organization_admin"}, {Kind: "deploy_key"},
		{Kind: "enterprise_owner"}, {Kind: "enterprise_role", ID: "role"},
		{Kind: "unknown", RawKind: "FuturePrincipal"},
	} {
		for _, mode := range []string{"always", "pull_request", "exempt"} {
			actor.Mode = mode
			if err := actor.Validate(); err != nil {
				t.Fatalf("observed actor rejected: %+v: %v", actor, err)
			}
		}
	}
	for _, actor := range []authority.BypassActor{
		{Kind: "user", Mode: "always"}, {Kind: "unknown", Mode: "always"},
		{Kind: "FuturePrincipal", Mode: "always"},
		{Kind: "deploy_key", Mode: "other"},
		{Kind: "organization_admin", ID: "95", Mode: "always"},
		{Kind: "deploy_key", ID: "95", Mode: "always"},
		{Kind: "enterprise_owner", ID: "95", Mode: "always"},
		{Kind: "team", ID: "200", Mode: "always", RawKind: "Team"},
	} {
		if actor.Validate() == nil {
			t.Fatalf("malformed actor accepted: %+v", actor)
		}
	}
}

func TestInheritedConditionAndNestedRuleFingerprints(t *testing.T) {
	for _, kind := range []string{"repository_name", "repository_id", "repository_property"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile("../queue/testdata/wire/v1/variants/write_restriction_verified_" + kind + ".json")
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
			o := fixture.Data.Evidence
			want, err := o.Fingerprint()
			if err != nil || want != fixture.Data.Fingerprint {
				t.Fatal("invalid inherited fixture", err)
			}
			// Every condition leaf, including protected, property source and
			// exclusions, and every nested rule leaf participates in identity.
			params := append([]authority.Parameter{}, o.Protections[0].Conditions...)
			for i := range params {
				changed := authority.CloneObservation(o)
				changed.Protections[0].Conditions[i].Values[0] += "-changed"
				got, err := changed.Fingerprint()
				if err != nil || got == want {
					t.Fatal("condition omitted from fingerprint", params[i].Name, err)
				}
			}
			ruleIndex := len(o.Protections[0].Rules) - 1
			for i, p := range o.Protections[0].Rules[ruleIndex].Parameters {
				changed := authority.CloneObservation(o)
				changed.Protections[0].Rules[ruleIndex].Parameters[i].Values[0] += "-changed"
				got, err := changed.Fingerprint()
				if err != nil || got == want {
					t.Fatal("nested rule parameter omitted from fingerprint", p.Name, err)
				}
			}
		})
	}
}

func TestObservationNestedCollectionBounds(t *testing.T) {
	stringsOf := func(n int) []string {
		values := make([]string, n)
		for i := range values {
			values[i] = fmt.Sprint(i)
		}
		return values
	}
	parameters := func(n int) []authority.Parameter {
		values := make([]authority.Parameter, n)
		for i := range values {
			values[i] = authority.Parameter{Name: fmt.Sprint(i), Values: []string{"value"}}
		}
		return values
	}
	for name, fill := range map[string]func(*authority.Observation, int){
		"includes":   func(o *authority.Observation, n int) { o.Protections[0].Includes = stringsOf(n) },
		"excludes":   func(o *authority.Observation, n int) { o.Protections[0].Excludes = stringsOf(n) },
		"conditions": func(o *authority.Observation, n int) { o.Protections[0].Conditions = parameters(n) },
		"condition values": func(o *authority.Observation, n int) {
			o.Protections[0].Conditions = []authority.Parameter{{Name: "repository_id/repository_ids", Values: stringsOf(n)}}
		},
		"rules": func(o *authority.Observation, n int) {
			o.Protections[0].Rules = make([]authority.Rule, n)
			for i := range o.Protections[0].Rules {
				o.Protections[0].Rules[i] = authority.Rule{Kind: "creation", Parameters: []authority.Parameter{}}
			}
		},
		"rule parameters": func(o *authority.Observation, n int) { o.Protections[0].Rules[0].Parameters = parameters(n) },
		"rule values": func(o *authority.Observation, n int) {
			o.Protections[0].Rules[0].Parameters = []authority.Parameter{{Name: "contexts", Values: stringsOf(n)}}
		},
		"bypass actors": func(o *authority.Observation, n int) {
			o.Protections[0].BypassActors = make([]authority.BypassActor, n)
			for i := range o.Protections[0].BypassActors {
				o.Protections[0].BypassActors[i] = authority.BypassActor{Kind: "user", ID: fmt.Sprint(i), Mode: "always"}
			}
		},
		"coverage protection IDs": func(o *authority.Observation, n int) {
			p := o.Protections[0]
			o.Protections = make([]authority.Protection, 1000)
			ids := make([]string, 1000)
			for i := range ids {
				o.Protections[i] = p
				o.Protections[i].ID = fmt.Sprint(i)
				ids[i] = p.Source + "/" + fmt.Sprint(i)
			}
			o.Coverage = []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: ids}}
			if n > 1000 {
				o.Coverage[0].ProtectionIDs = append(ids, ids[0])
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, n := range []int{1000, 1001} {
				o, _ := seeingEvidence(t)
				fill(&o, n)
				err := o.Validate()
				if (err == nil) != (n == 1000) {
					t.Fatalf("size %d: %v", n, err)
				}
			}
		})
	}
}
