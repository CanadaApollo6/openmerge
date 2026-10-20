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
			p := authority.Protection{Source: "repository_ruleset", ID: "10", Target: "branch", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible,
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
			if strings.HasPrefix(scenario, "target-") {
				p.Target = strings.TrimPrefix(scenario, "target-")
			}
			switch scenario {
			case "unauthorized-actors":
				p.BypassActors = []authority.BypassActor{{Kind: "user", ID: "91", Mode: "always"}, {Kind: "deploy_key", Mode: "always"}, {Kind: "organization_admin", Mode: "exempt"}, {Kind: "team", ID: "92", Mode: "exempt"}}
			case "class-wide-actors", "class-wide-actors-with-id":
				p.BypassActors = []authority.BypassActor{{Kind: "organization_admin", Mode: "always"}, {Kind: "deploy_key", Mode: "always"}, {Kind: "enterprise_owner", Mode: "always"}}
				if scenario == "class-wide-actors-with-id" {
					for i := range p.BypassActors {
						p.BypassActors[i].ID = "95"
					}
				}
			case "unknown-actor":
				p.BypassActors = []authority.BypassActor{{Kind: "unknown", RawKind: "FutureActor", Mode: "pull_request"}, {Kind: "unknown", RawKind: "FutureActor", ID: "93", Mode: "exempt"}}
			case "overflow-includes", "overflow-excludes", "overflow-conditions", "overflow-condition-values":
				p.TargetsVisibility = authority.ReadFailure
				p.Includes, p.Excludes, p.Conditions = []string{}, []string{}, []authority.Parameter{}
				err = errors.New("target collection exceeds bound")
			case "overflow-rules", "overflow-rule-parameters", "overflow-rule-values":
				p.RulesVisibility, p.Rules = authority.ReadFailure, []authority.Rule{}
				err = errors.New("rule collection exceeds bound")
			case "overflow-bypass":
				p.BypassVisibility, p.BypassActors = authority.ReadFailure, []authority.BypassActor{}
				err = errors.New("bypass collection exceeds bound")
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
