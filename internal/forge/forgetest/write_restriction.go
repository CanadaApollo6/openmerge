package forgetest

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// WriteRestrictionEnv supplies equivalent independently acquired observations:
// fake state or a GitHub HTTP server. Configure changes only authority reads.
type WriteRestrictionEnv struct {
	Reader    forge.WriteRestrictionReader
	Configure func(scenario string)
	SetTeam   func(forge.TeamIdentity)
}

// WriteRestrictionInput is a fixed reviewed binding shared by both adapters.
func WriteRestrictionInput() forge.WriteRestrictionInput {
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	coverage, _ := authority.CoverageDigest("main", selectors)
	return forge.WriteRestrictionInput{
		Repo: forge.RepoRef{Path: "acme/project"}, AppID: "42", BaseSelectors: selectors,
		ExpectedRefs: []string{"refs/heads/main", "refs/heads/release/2026/oct", "refs/heads/openmerge/fence/release/2026/oct", "refs/heads/openmerge/queue/123"},
		Binding:      authority.PolicyBinding{InstallationID: 7, RepositoryID: 99, Repository: "acme/project", QueueKey: "main", EffectiveDigest: strings.Repeat("a", 64), CoverageDigest: coverage, DefaultBranch: "main", MergeMethod: "merge", DeclaredActor: authority.Actor{Team: "acme/platform"}, ResolvedActor: authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "55", TeamID: "44"}}},
	}
}

// RunWriteRestrictionReader extends the optional reader contracts; it neither
// broadens RunDriver's legacy scope nor treats fixtures as GitHub enforcement.
func RunWriteRestrictionReader(t *testing.T, factory func(*testing.T) WriteRestrictionEnv) {
	team := forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"}
	for _, tc := range []struct {
		name      string
		inventory authority.Visibility
		count     int
		bypass    authority.Visibility
		wantErr   bool
	}{
		{"visible", authority.Visible, 1, authority.Visible, false},
		{"visible-empty-bypass", authority.Visible, 1, authority.Visible, false},
		{"hidden-bypass", authority.Visible, 1, authority.Hidden, false},
		{"null-bypass", authority.Visible, 1, authority.Hidden, false},
		{"inherited", authority.Visible, 1, authority.Visible, false},
		{"inherited-hidden-bypass", authority.Visible, 1, authority.Hidden, false},
		{"inherited-detail-denied", authority.Visible, 1, authority.Hidden, true},
		{"empty", authority.Visible, 0, "", false},
		{"partial-inventory", authority.ReadFailure, 1, authority.Visible, true},
		{"inventory-denied", authority.ReadFailure, 0, "", true},
		{"inventory-not-found", authority.ReadFailure, 0, "", true},
		{"detail-denied", authority.Visible, 1, authority.Hidden, true},
		{"quota", authority.ReadFailure, 0, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := factory(t)
			e.SetTeam(team)
			e.Configure(tc.name)
			got, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, want error %v", err, tc.wantErr)
			}
			must(t, got.Validate())
			if got.Inventory != tc.inventory || len(got.Protections) != tc.count {
				t.Fatalf("inventory=%s protections=%+v", got.Inventory, got.Protections)
			}
			if len(got.Coverage) != 0 || len(got.Findings) != 0 {
				t.Fatal("reader interpreted protection instead of preserving facts")
			}
			if tc.count == 0 {
				return
			}
			p := got.Protections[0]
			if p.BypassVisibility != tc.bypass {
				t.Fatalf("bypass visibility=%s", p.BypassVisibility)
			}
			if strings.HasPrefix(tc.name, "inherited") && p.Source != "inherited_ruleset" || !strings.HasPrefix(tc.name, "inherited") && p.Source != "repository_ruleset" {
				t.Fatalf("source=%s", p.Source)
			}
			if strings.HasSuffix(tc.name, "detail-denied") {
				if p.TargetsVisibility != authority.Hidden || p.RulesVisibility != authority.Hidden || p.Enforcement != "active" {
					t.Fatalf("lost independently visible list evidence: %+v", p)
				}
				return
			}
			if p.ID != "10" || p.Target != "branch" || p.Enforcement != "active" || p.TargetsVisibility != authority.Visible || p.RulesVisibility != authority.Visible || !reflect.DeepEqual(p.Includes, []string{"refs/heads/main", "refs/heads/release/**/*"}) || len(p.Excludes) != 0 {
				t.Fatalf("lost ruleset detail: %+v", p)
			}
			want := []authority.Rule{{Kind: "update", Parameters: []authority.Parameter{{Name: "update_allows_fetch_and_merge", Values: []string{"false"}}}}}
			if !reflect.DeepEqual(p.Rules, want) {
				t.Fatalf("parameters=%+v", p.Rules)
			}
			if tc.name == "visible-empty-bypass" || tc.bypass == authority.Hidden {
				if len(p.BypassActors) != 0 {
					t.Fatalf("invented bypass: %+v", p.BypassActors)
				}
			} else if !reflect.DeepEqual(p.BypassActors, []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}, {Kind: "team", ID: "44", Mode: "always"}, {Kind: "repository_role", ID: "5", Mode: "pull_request"}}) {
				t.Fatalf("actors=%+v", p.BypassActors)
			}
			// Returned evidence and fixture state must not share mutable slices.
			got.Protections[0].Rules[0].Parameters[0].Values[0] = "mutated"
			again, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			if (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
			if again.Protections[0].Rules[0].Parameters[0].Values[0] != "false" {
				t.Fatal("observation aliases earlier result")
			}
		})
	}
	for _, target := range []string{"branch", "tag", "push", "repository"} {
		t.Run("target-"+target, func(t *testing.T) {
			e := factory(t)
			e.SetTeam(team)
			e.Configure("target-" + target)
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			must(t, err)
			must(t, o.Validate())
			if len(o.Protections) != 1 || o.Protections[0].Target != target || o.Protections[0].TargetsVisibility != authority.Visible {
				t.Fatalf("lost target: %+v", o)
			}
		})
	}
	for scenario, want := range map[string][]authority.BypassActor{
		"unauthorized-actors": {{Kind: "user", ID: "91", Mode: "always"}, {Kind: "deploy_key", Mode: "always"}, {Kind: "organization_admin", Mode: "exempt"}, {Kind: "team", ID: "92", Mode: "exempt"}},
		"unknown-actor":       {{Kind: "unknown", RawKind: "FutureActor", Mode: "pull_request"}, {Kind: "unknown", RawKind: "FutureActor", ID: "93", Mode: "exempt"}},
	} {
		t.Run(scenario, func(t *testing.T) {
			e := factory(t)
			e.SetTeam(team)
			e.Configure(scenario)
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			must(t, err)
			must(t, o.Validate())
			if len(o.Protections) != 1 || o.Protections[0].BypassVisibility != authority.Visible || !reflect.DeepEqual(o.Protections[0].BypassActors, want) || scenario == "unknown-actor" && len(o.Findings) != len(want) || scenario != "unknown-actor" && len(o.Findings) != 0 {
				t.Fatalf("reader rejected or interpreted observed actors: %+v", o)
			}
		})
	}
	t.Run("class-wide-actor-identities", func(t *testing.T) {
		e := factory(t)
		e.SetTeam(team)
		want := []authority.BypassActor{{Kind: "organization_admin", Mode: "always"}, {Kind: "deploy_key", Mode: "always"}, {Kind: "enterprise_owner", Mode: "always"}}
		var fingerprint string
		for _, scenario := range []string{"class-wide-actors", "class-wide-actors-with-id"} {
			e.Configure(scenario)
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			must(t, err)
			must(t, o.Validate())
			if len(o.Protections) != 1 || !reflect.DeepEqual(o.Protections[0].BypassActors, want) {
				t.Fatalf("class-wide IDs were retained: %+v", o)
			}
			if len(o.Coverage) != 0 {
				t.Fatal("reader interpreted coverage")
			}
			// Supply synthetic coverage only to exercise the fingerprint contract;
			// ref interpretation remains outside the reader.
			o.Coverage = []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/10"}}}
			got, err := o.Fingerprint()
			must(t, err)
			if fingerprint != "" && got != fingerprint {
				t.Fatalf("ignored actor IDs changed fingerprint: %s != %s", got, fingerprint)
			}
			fingerprint = got
		}
	})
	for _, field := range []string{"includes", "excludes", "conditions", "condition-values", "rules", "rule-parameters", "rule-values", "bypass"} {
		t.Run("overflow-"+field, func(t *testing.T) {
			e := factory(t)
			e.SetTeam(team)
			e.Configure("overflow-" + field)
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			if err == nil {
				t.Fatal("overflow accepted")
			}
			must(t, o.Validate())
			if o.Inventory != authority.Visible || len(o.Protections) != 1 {
				t.Fatalf("lost inventory: %+v", o)
			}
			p := o.Protections[0]
			targets, rules, bypass := authority.Visible, authority.Visible, authority.Visible
			switch field {
			case "includes", "excludes", "conditions", "condition-values":
				targets = authority.ReadFailure
			case "rules", "rule-parameters", "rule-values":
				rules = authority.ReadFailure
			case "bypass":
				bypass = authority.ReadFailure
			}
			if p.TargetsVisibility != targets || p.RulesVisibility != rules || p.BypassVisibility != bypass {
				t.Fatalf("overflow erased independent evidence: %+v", p)
			}
		})
	}
	t.Run("fresh-actor-binding", func(t *testing.T) {
		e := factory(t)
		e.SetTeam(team)
		e.Configure("visible")
		_, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
		must(t, err)
		for _, changed := range []forge.TeamIdentity{
			{ID: 45, OrganizationID: 55, Organization: "acme", Slug: "platform"},
			{ID: 44, OrganizationID: 56, Organization: "acme", Slug: "platform"},
		} {
			e.SetTeam(changed)
			got, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
			if !errors.Is(err, forge.ErrIdentityMismatch) || got.Inventory != authority.ReadFailure || len(got.Protections) != 1 {
				t.Fatalf("reassigned team reused or evidence lost: %+v %v", got, err)
			}
		}
	})
	t.Run("actor-unavailable", func(t *testing.T) {
		e := factory(t)
		e.Configure("visible")
		got, err := e.Reader.ObserveWriteRestriction(t.Context(), WriteRestrictionInput())
		if err == nil || got.Inventory != authority.ReadFailure || len(got.Protections) != 1 {
			t.Fatalf("unresolved actor: %+v %v", got, err)
		}
	})
	t.Run("repository-admin", func(t *testing.T) {
		e := factory(t)
		e.Configure("visible")
		in := WriteRestrictionInput()
		in.Binding.DeclaredActor = authority.Actor{RepositoryAdmin: true}
		in.Binding.ResolvedActor = authority.ResolvedActor{RepositoryAdmin: &authority.ResolvedRepositoryAdmin{RoleID: "5"}}
		_, err := e.Reader.ObserveWriteRestriction(t.Context(), in)
		must(t, err)
	})
	t.Run("unsupported-repository-admin-role", func(t *testing.T) {
		e := factory(t)
		e.Configure("visible")
		in := WriteRestrictionInput()
		in.Binding.DeclaredActor = authority.Actor{RepositoryAdmin: true}
		in.Binding.ResolvedActor = authority.ResolvedActor{RepositoryAdmin: &authority.ResolvedRepositoryAdmin{RoleID: "999"}}
		o, err := e.Reader.ObserveWriteRestriction(t.Context(), in)
		if !errors.Is(err, forge.ErrIdentityMismatch) || o.Inventory != authority.ReadFailure || len(o.Protections) != 0 {
			t.Fatalf("unsupported admin role read facts: %+v %v", o, err)
		}
	})
	t.Run("invalid-input", func(t *testing.T) {
		e := factory(t)
		e.SetTeam(team)
		e.Configure("visible")
		for _, edit := range []func(*forge.WriteRestrictionInput){
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/bad..ref"} },
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/tags/main"} },
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"main"} },
			func(in *forge.WriteRestrictionInput) { in.Binding.CoverageDigest = strings.Repeat("b", 64) },
			func(in *forge.WriteRestrictionInput) { in.Binding.InstallationID = 0 },
			func(in *forge.WriteRestrictionInput) { in.Binding.DefaultBranch = "bad..branch" },
			func(in *forge.WriteRestrictionInput) {
				in.ExpectedRefs = make([]string, 1001)
				for i := range in.ExpectedRefs {
					in.ExpectedRefs[i] = "refs/heads/branch-" + strconv.Itoa(i)
				}
			},
		} {
			in := WriteRestrictionInput()
			edit(&in)
			got, err := e.Reader.ObserveWriteRestriction(t.Context(), in)
			if !errors.Is(err, forge.ErrInvalidInput) || got.Inventory != authority.ReadFailure || len(got.Protections) != 0 {
				t.Fatalf("invalid input read facts: %+v %v", got, err)
			}
		}
	})
}
