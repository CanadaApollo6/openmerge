package forgetest

import (
	"errors"
	"reflect"
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
			if p.ID != "10" || p.Enforcement != "active" || p.TargetsVisibility != authority.Visible || p.RulesVisibility != authority.Visible || !reflect.DeepEqual(p.Includes, []string{"refs/heads/main", "refs/heads/release/**/*"}) || len(p.Excludes) != 0 {
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
	t.Run("invalid-input", func(t *testing.T) {
		e := factory(t)
		e.SetTeam(team)
		e.Configure("visible")
		for _, edit := range []func(*forge.WriteRestrictionInput){
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/heads/bad..ref"} },
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"refs/tags/main"} },
			func(in *forge.WriteRestrictionInput) { in.ExpectedRefs = []string{"main"} },
			func(in *forge.WriteRestrictionInput) { in.Binding.CoverageDigest = strings.Repeat("b", 64) },
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
