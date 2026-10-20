package fake

import (
	"errors"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestWriteRestrictionContract(t *testing.T) {
	forgetest.RunWriteRestrictionReader(t, func(t *testing.T) forgetest.WriteRestrictionEnv {
		f := New()
		return forgetest.WriteRestrictionEnv{Reader: f, SetTeam: func(team forge.TeamIdentity) { f.SetTeam(team, nil) }, Configure: func(scenario string) {
			o := forge.EmptyWriteRestrictionObservation()
			o.Inventory = authority.Visible
			p := authority.Protection{Source: "repository_ruleset", ID: "10", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible,
				Includes: []string{"refs/heads/main", "refs/heads/release/**/*"}, Excludes: []string{}, Conditions: []authority.Parameter{},
				Rules:        []authority.Rule{{Kind: "update", Parameters: []authority.Parameter{{Name: "update_allows_fetch_and_merge", Values: []string{"false"}}}}},
				BypassActors: []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}, {Kind: "team", ID: "44", Mode: "always"}, {Kind: "repository_role", ID: "5", Mode: "pull_request"}}}
			if strings.HasPrefix(scenario, "inherited") {
				p.Source = "inherited_ruleset"
			}
			var err error
			switch scenario {
			case "hidden-bypass", "null-bypass", "inherited-hidden-bypass":
				p.BypassVisibility = authority.Hidden
				p.BypassActors = []authority.BypassActor{}
			case "visible-empty-bypass":
				p.BypassActors = []authority.BypassActor{}
			case "inherited":
				p.Source = "inherited_ruleset"
			case "partial-inventory":
				o.Inventory = authority.ReadFailure
				err = errors.New("later page failed")
			case "detail-denied", "inherited-detail-denied":
				p.TargetsVisibility, p.RulesVisibility, p.BypassVisibility = authority.Hidden, authority.Hidden, authority.Hidden
				p.Includes = []string{}
				p.Rules = []authority.Rule{}
				p.BypassActors = []authority.BypassActor{}
				err = forge.ErrRulesUnavailable
			case "inventory-denied", "inventory-not-found", "quota":
				o.Inventory = authority.ReadFailure
				err = forge.ErrRulesUnavailable
			}
			switch scenario {
			case "empty", "inventory-denied", "inventory-not-found", "quota":
			default:
				o.Protections = append(o.Protections, p)
			}
			if err := f.SetWriteRestrictionObservation(forgetest.WriteRestrictionInput().Repo, o, err); err != nil {
				t.Fatal(err)
			}
		}}
	})
}

func TestUnconfiguredAuthorityIsUnknown(t *testing.T) {
	f := New()
	o, err := f.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
	if !errors.Is(err, forge.ErrRulesUnavailable) || o.Inventory != authority.ReadFailure {
		t.Fatalf("unconfigured protection became absent: %+v %v", o, err)
	}
}
