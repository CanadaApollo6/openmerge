package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func authorityCandidateSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("testdata/authority.candidate.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.RegisterFormat(&jsonschema.Format{Name: "openmerge-authority-team", Validate: func(value any) error {
		text, ok := value.(string)
		if !ok || (authority.Actor{Team: text}).Validate() != nil {
			return fmt.Errorf("invalid authority team")
		}
		return nil
	}})
	if err := compiler.AddResource("authority-candidate.json", doc); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("authority-candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestAuthorityCandidateBaseSelectors(t *testing.T) {
	data, err := os.ReadFile("testdata/authority.yml.candidate")
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := LoadAuthorityCandidate(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	if got := candidate.BaseSelectors(); !reflect.DeepEqual(got, want) {
		t.Fatalf("base selectors = %#v, want %#v", got, want)
	}
}

func TestAuthorityCandidateSchemaAgreement(t *testing.T) {
	future := authorityCandidateSchema(t)
	for _, tc := range []struct {
		name, suffix string
		valid        bool
		mode         authority.Mode
		actor        authority.Actor
	}{
		{"omitted", "", true, authority.GitHub, authority.Actor{}},
		{"github", "policy_authority: github\n", true, authority.GitHub, authority.Actor{}},
		{"openmerge team", "policy_authority: openmerge\nbreak_glass: {team: Smart-Data-Ohio/break-glass}\n", true, authority.OpenMerge, authority.Actor{Team: "Smart-Data-Ohio/break-glass"}},
		{"openmerge repository admin", "policy_authority: openmerge\nbreak_glass: {repository_admin: true}\n", true, authority.OpenMerge, authority.Actor{RepositoryAdmin: true}},
		{"break glass omitted mode", "break_glass: {team: Smart-Data-Ohio/break-glass}\n", false, "", authority.Actor{}},
		{"github break glass", "policy_authority: github\nbreak_glass: {team: Smart-Data-Ohio/break-glass}\n", false, "", authority.Actor{}},
		{"openmerge missing break glass", "policy_authority: openmerge\n", false, "", authority.Actor{}},
		{"unknown mode", "policy_authority: future\n", false, "", authority.Actor{}},
		{"empty mode", "policy_authority: ''\n", false, "", authority.Actor{}},
		{"false repository admin", "policy_authority: openmerge\nbreak_glass: {repository_admin: false}\n", false, "", authority.Actor{}},
		{"both actors", "policy_authority: openmerge\nbreak_glass: {team: Smart-Data-Ohio/break-glass, repository_admin: true}\n", false, "", authority.Actor{}},
		{"empty team beside role", "policy_authority: openmerge\nbreak_glass: {team: '', repository_admin: true}\n", false, "", authority.Actor{}},
		{"null team", "policy_authority: openmerge\nbreak_glass: {team: null}\n", false, "", authority.Actor{}},
		{"user actor", "policy_authority: openmerge\nbreak_glass: {user: alice}\n", false, "", authority.Actor{}},
		{"bot actor", "policy_authority: openmerge\nbreak_glass: {bot: openmerge}\n", false, "", authority.Actor{}},
		{"unknown actor key", "policy_authority: openmerge\nbreak_glass: {team: Smart-Data-Ohio/break-glass, extra: true}\n", false, "", authority.Actor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n" + tc.suffix)
			candidate, _, candidateErr := LoadAuthorityCandidate(data)
			_, _, originalLoadErr := Load(data)
			assertSchemaAgreement(t, data, originalLoadErr)
			raw, inputErr := schemaInput(data)
			var futureErr error
			if inputErr != nil {
				futureErr = inputErr
			} else {
				futureErr = future.Validate(raw)
			}
			publicData, publicErr := authorityCandidatePublicYAML(data)
			if publicErr == nil {
				_, _, publicErr = Load(publicData)
				assertSchemaAgreement(t, publicData, publicErr)
			}
			valid := candidateErr == nil
			if valid != tc.valid || (futureErr == nil && publicErr == nil) != tc.valid {
				t.Fatalf("candidate/schema disagreement; candidate=%v future=%v public=%v", candidateErr, futureErr, publicErr)
			}
			if tc.valid {
				if candidate.Authority.Mode.Normalized() != tc.mode || candidate.Authority.BreakGlass == nil && tc.actor != (authority.Actor{}) || candidate.Authority.BreakGlass != nil && *candidate.Authority.BreakGlass != tc.actor {
					t.Fatalf("candidate authority = %+v, want mode=%s actor=%+v", candidate.Authority, tc.mode, tc.actor)
				}
				if tc.suffix != "" {
					if _, _, err := Load(data); err == nil {
						t.Fatal("public loader accepted a future authority key")
					}
				}
			}
		})
	}
}

func TestAuthorityCandidateRejectsDuplicateFutureRootKeys(t *testing.T) {
	data := []byte(`version: 1
policy_authority: github
policy_authority: openmerge
break_glass: {team: Smart-Data-Ohio/break-glass}
queues:
- name: main
  merge_when: [check-success = ci]
`)
	if _, _, err := LoadAuthorityCandidate(data); err == nil {
		t.Fatal("candidate loader accepted duplicate policy_authority")
	}
}
