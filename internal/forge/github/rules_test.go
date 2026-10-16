package github

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func fixtureRules(t *testing.T) []Ruleset {
	t.Helper()
	data, err := os.ReadFile("testdata/rest/rulesets.json")
	must(t, err)
	var rules []Ruleset
	must(t, json.Unmarshal(data, &rules))
	return rules
}

func TestValidateRules(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func([]Ruleset) []Ruleset
		want []RuleProblemCode
	}{
		{"valid", func(r []Ruleset) []Ruleset { return r }, nil},
		{"unprotected base needs no bypass", func(r []Ruleset) []Ruleset { return r[1:] }, nil},
		{"wrong App", func(r []Ruleset) []Ruleset { r[0].BypassActors[0].ActorID = new(int64(7)); return r }, []RuleProblemCode{BaseBypassMissing}},
		{"PR-only bypass cannot land", func(r []Ruleset) []Ruleset { r[0].BypassActors[0].BypassMode = "pull_request"; return r }, []RuleProblemCode{BaseBypassMissing}},
		{"inherited base", func(r []Ruleset) []Ruleset {
			r[0].SourceType = "Organization"
			r[0].BypassActors = []BypassActor{}
			return r
		}, []RuleProblemCode{BaseBypassMissing}},
		{"fence has other bypass", func(r []Ruleset) []Ruleset {
			r[1].BypassActors = append(r[1].BypassActors, r[0].BypassActors[1])
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"fence missing deletion", func(r []Ruleset) []Ruleset { r[1].Rules = append(r[1].Rules[:2], r[1].Rules[3:]...); return r }, []RuleProblemCode{FenceUnprotected}},
		{"fence disabled", func(r []Ruleset) []Ruleset { r[1].Enforcement = "disabled"; return r }, []RuleProblemCode{FenceUnprotected}},
		{"fence evaluated", func(r []Ruleset) []Ruleset { r[1].Enforcement = "evaluate"; return r }, []RuleProblemCode{FenceUnprotected}},
		{"nested fences unprotected", func(r []Ruleset) []Ruleset {
			r[1].Conditions = json.RawMessage(`{"ref_name":{"include":["refs/heads/openmerge/fence/*"],"exclude":[]}}`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"excluded fence", func(r []Ruleset) []Ruleset {
			r[1].Conditions = json.RawMessage(`{"ref_name":{"include":["refs/heads/openmerge/fence/**/*"],"exclude":["refs/heads/openmerge/fence/release/*"]}}`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"fence permits upstream updates", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`{"update_allows_fetch_and_merge":true}`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		// GitHub omits update parameters when fetch-and-merge is false (its default).
		{"fence update parameters omitted", func(r []Ruleset) []Ruleset { r[1].Rules[1].Parameters = nil; return r }, nil},
		{"fence update parameters null", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`null`)
			return r
		}, nil},
		{"fence update parameters malformed JSON", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`{"update_allows_fetch_and_merge":`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"fence update parameters wrong type", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`{"update_allows_fetch_and_merge":"false"}`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"fence update parameters array", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`[]`)
			return r
		}, []RuleProblemCode{FenceUnprotected}},
		{"fence update parameters without the field", func(r []Ruleset) []Ruleset {
			r[1].Rules[1].Parameters = json.RawMessage(`{}`)
			return r
		}, nil},
		{"missing rule details", func(r []Ruleset) []Ruleset { r[0].Rules = nil; return r }, []RuleProblemCode{RulesUnknown}},
		{"hidden bypass actors", func(r []Ruleset) []Ruleset { r[0].BypassActors = nil; return r }, []RuleProblemCode{RulesUnknown}},
		{"unknown conditions", func(r []Ruleset) []Ruleset { r[0].Conditions = json.RawMessage(`{}`); return r }, []RuleProblemCode{RulesUnknown}},
		{"malformed pattern", func(r []Ruleset) []Ruleset {
			r[0].Conditions = json.RawMessage(`{"ref_name":{"include":["refs/heads/["],"exclude":[]}}`)
			return r
		}, []RuleProblemCode{RulesUnknown}},
		{"queue blocked", func(r []Ruleset) []Ruleset {
			r[0].Conditions = json.RawMessage(`{"ref_name":{"include":["refs/heads/openmerge/queue/main/*"],"exclude":[]}}`)
			r[0].BypassActors = []BypassActor{}
			return r
		}, []RuleProblemCode{RefBlocked}},
		{"all branches blocked", func(r []Ruleset) []Ruleset {
			r[0].Conditions = json.RawMessage(`{"ref_name":{"include":["~ALL"],"exclude":[]}}`)
			r[0].BypassActors = []BypassActor{}
			return r
		}, []RuleProblemCode{BaseBypassMissing, RefBlocked, RefBlocked}},
		{"queue excluded", func(r []Ruleset) []Ruleset {
			r[0].Conditions = json.RawMessage(`{"ref_name":{"include":["refs/heads/openmerge/**/*"],"exclude":["refs/heads/openmerge/queue/**/*","refs/heads/openmerge/fence/**/*"]}}`)
			r[0].BypassActors = []BypassActor{}
			return r
		}, nil},
		{"push rules apply everywhere", func(r []Ruleset) []Ruleset { r[0].Target = "push"; r[0].BypassActors = []BypassActor{}; return r }, []RuleProblemCode{BaseBypassMissing, RefBlocked, RefBlocked}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateRules(tt.edit(fixtureRules(t)), "main", "main", 42)
			codes := make([]RuleProblemCode, 0, len(problems))
			for _, p := range problems {
				codes = append(codes, p.Code)
				if p.Error() == "" || p.Ref == "" && p.Code != RulesUnknown {
					t.Errorf("not actionable: %#v", p)
				}
			}
			if !slices.Equal(codes, tt.want) {
				t.Errorf("problems = %v, want %v (%v)", codes, tt.want, problems)
			}
		})
	}
}

func TestRulesetPathMatching(t *testing.T) {
	for _, tt := range []struct {
		pattern, ref string
		want         bool
	}{
		{"refs/heads/*", "refs/heads/release/v1", false},
		{"refs/heads/release/**/v?", "refs/heads/release/a/b/v1", true},
		{"refs/heads/openmerge/fence/**/*", "refs/heads/openmerge/fence/main", true},
		{"refs/heads/openmerge/fence/**/*", "refs/heads/openmerge/fence/release/v1", true},
		{"refs/heads/openmerge/**", "refs/heads/openmerge/queue/main/batch", false},
		{"refs/heads/[mr]*", "refs/heads/main", true},
		{"~DEFAULT_BRANCH", "refs/heads/main", true},
	} {
		if got := matches(tt.pattern, tt.ref, "refs/heads/main"); got != tt.want {
			t.Errorf("matches(%q,%q) = %v", tt.pattern, tt.ref, got)
		}
	}
}

func TestValidateRulesDistinguishesDefaultFromLandingBase(t *testing.T) {
	for _, tt := range []struct {
		name, base, conditions string
		want                   []RuleProblemCode
	}{
		{"release excludes default", "release/v1", `{"ref_name":{"include":["refs/heads/release/v1"],"exclude":["~DEFAULT_BRANCH"]}}`, []RuleProblemCode{BaseBypassMissing}},
		{"default only does not include release", "release/v1", `{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}}`, nil},
		{"default only includes main", "main", `{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}}`, []RuleProblemCode{BaseBypassMissing}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rules := fixtureRules(t)
			rules[0].Conditions = json.RawMessage(tt.conditions)
			rules[0].BypassActors = []BypassActor{}
			problems := ValidateRules(rules, tt.base, "main", 42)
			var codes []RuleProblemCode
			for _, p := range problems {
				codes = append(codes, p.Code)
				if p.Code == BaseBypassMissing && p.Ref != "refs/heads/"+tt.base {
					t.Errorf("wrong landing ref: %+v", p)
				}
			}
			if !slices.Equal(codes, tt.want) {
				t.Fatalf("problems = %+v; want %v", problems, tt.want)
			}
		})
	}
	if problems := ValidateRules(fixtureRules(t), "release/v1", "", 42); len(problems) != 1 || problems[0].Code != RulesUnknown {
		t.Fatalf("unknown default did not fail closed: %+v", problems)
	}
}
