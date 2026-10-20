package github

import (
	"errors"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/authority"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func authorityHTTPEnv(t *testing.T) (*apiFixture, forgetest.WriteRestrictionEnv) {
	t.Helper()
	f := newAPI(t)
	scenario := "visible"
	var team forge.TeamIdentity
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
		if strings.HasPrefix(path, "app/") || path == "app" {
			return false
		}
		if r.Method != "GET" {
			t.Errorf("authority mutated remote state: %s %s", r.Method, path)
		}
		if path == "repos/acme/project" {
			f.json(w, map[string]any{"id": 99, "default_branch": "main"})
			return true
		}
		if path == "orgs/acme/teams/platform" {
			if team.ID == 0 {
				f.apiError(w, 404, "Not Found")
			} else {
				f.json(w, map[string]any{"id": team.ID, "slug": team.Slug, "organization": map[string]any{"id": team.OrganizationID, "login": team.Organization}})
			}
			return true
		}
		if !strings.HasPrefix(path, "repos/acme/project/rulesets") {
			t.Errorf("unexpected authority source read: %s", path)
			return false
		}
		if r.URL.Query().Get("includes_parents") != "true" {
			t.Error("inherited rulesets not requested")
		}
		sourceType, source := "Repository", "acme/project"
		if strings.HasPrefix(scenario, "inherited") {
			sourceType, source = "Organization", "acme"
		}
		summary := map[string]any{"id": 10, "source_type": sourceType, "source": source, "enforcement": "active"}
		if path == "repos/acme/project/rulesets" {
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("wrong page size")
			}
			switch scenario {
			case "empty":
				f.json(w, []any{})
				return true
			case "inventory-denied":
				f.apiError(w, 403, "insufficient permission")
				return true
			case "inventory-not-found":
				f.apiError(w, 404, "Not Found")
				return true
			case "quota":
				w.Header().Set("Retry-After", "60")
				f.apiError(w, 429, "secondary rate limit")
				return true
			case "partial-inventory":
				if r.URL.Query().Get("page") == "2" {
					f.apiError(w, 403, "page two inaccessible")
					return true
				}
				w.Header().Set("Link", fmt.Sprintf(`<%s%s?includes_parents=true&per_page=100&page=2>; rel="next"`, f.server.URL, r.URL.Path))
			}
			f.json(w, []any{summary})
			return true
		}
		if path != "repos/acme/project/rulesets/10" {
			t.Fatalf("unexpected detail %s", path)
		}
		if strings.HasSuffix(scenario, "detail-denied") {
			f.apiError(w, 403, "inherited detail inaccessible")
			return true
		}
		summary["target"] = "branch"
		summary["updated_at"] = "2026-10-20T00:00:00Z"
		summary["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main", "refs/heads/release/**/*"}, "exclude": []string{}}}
		summary["rules"] = []any{map[string]any{"type": "update", "parameters": map[string]any{"update_allows_fetch_and_merge": false}}}
		switch scenario {
		case "hidden-bypass", "inherited-hidden-bypass":
		case "null-bypass":
			summary["bypass_actors"] = nil
		case "visible-empty-bypass":
			summary["bypass_actors"] = []any{}
		default:
			summary["bypass_actors"] = []any{
				map[string]any{"actor_type": "Integration", "actor_id": 42, "bypass_mode": "always"},
				map[string]any{"actor_type": "Team", "actor_id": 44, "bypass_mode": "always"},
				map[string]any{"actor_type": "RepositoryRole", "actor_id": 5, "bypass_mode": "pull_request"},
			}
		}
		f.json(w, summary)
		return true
	}
	return f, forgetest.WriteRestrictionEnv{Reader: f.driver, Configure: func(s string) { scenario = s }, SetTeam: func(value forge.TeamIdentity) { team = value }}
}

func TestWriteRestrictionContract(t *testing.T) {
	forgetest.RunWriteRestrictionReader(t, func(t *testing.T) forgetest.WriteRestrictionEnv { _, e := authorityHTTPEnv(t); return e })
}

func TestAuthorityQuotaRetainsRetryClass(t *testing.T) {
	f, e := authorityHTTPEnv(t)
	e.Configure("quota")
	_, err := f.driver.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
	var quota *RateLimitError
	if !errors.As(err, &quota) || !quota.Secondary || !quota.RetryAt.After(f.clock) {
		t.Fatalf("quota lost retry semantics: %v", err)
	}
}

func TestAuthorityReadsPreserveSettlementBudget(t *testing.T) {
	f, e := authorityHTTPEnv(t)
	e.Configure("visible")
	_, err := f.app.token(t.Context(), 7)
	must(t, err)
	tr := f.driver.client.Client().Transport.(*transport)
	tr.mu.Lock()
	tr.budgetKnown, tr.limit, tr.remaining, tr.reset = true, 1000, 100, epoch.Add(time.Hour)
	tr.mu.Unlock()
	_, err = f.driver.ObserveWriteRestriction(forge.WithRequestClass(t.Context(), forge.SettlementRequest), forgetest.WriteRestrictionInput())
	var rate *RateLimitError
	if !errors.As(err, &rate) || f.driver.Budget().Remaining != 100 {
		t.Fatalf("authority read consumed settlement reserve: %v %+v", err, f.driver.Budget())
	}
}

func TestAuthorityConditionalReadsKeepLegacyValidationIndependent(t *testing.T) {
	f, e := authorityHTTPEnv(t)
	e.Configure("hidden-bypass")
	e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
	beforeProblems, beforeWarnings, err := f.driver.ValidateRepositoryRules(t.Context(), testRepo, "main")
	must(t, err)
	if len(beforeWarnings) != 1 {
		t.Fatalf("legacy hidden-bypass warning missing: %v", beforeWarnings)
	}
	for range 2 {
		got, err := f.driver.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
		must(t, err)
		if got.Protections[0].BypassVisibility != authority.Hidden || len(got.Protections[0].BypassActors) != 0 {
			t.Fatal("legacy App substitution leaked into authority evidence")
		}
	}
	afterProblems, afterWarnings, err := f.driver.ValidateRepositoryRules(t.Context(), testRepo, "main")
	must(t, err)
	if !reflect.DeepEqual(beforeProblems, afterProblems) || !reflect.DeepEqual(beforeWarnings, afterWarnings) || f.notModified == 0 {
		t.Fatalf("legacy validation or conditional reads changed: %v %v", afterProblems, afterWarnings)
	}
	problems, warnings, err := f.driver.ValidateInstallationPermissions(t.Context())
	must(t, err)
	if len(problems)+len(warnings) != 0 {
		t.Fatalf("optional Administration read became a legacy grant requirement: %v %v", problems, warnings)
	}
}

func TestAuthorityRejectsBindingMismatchBeforeReading(t *testing.T) {
	for _, edit := range []func(*forge.WriteRestrictionInput){
		func(in *forge.WriteRestrictionInput) { in.AppID = "999" },
		func(in *forge.WriteRestrictionInput) { in.Binding.InstallationID = 8 },
		func(in *forge.WriteRestrictionInput) {
			in.Binding.DeclaredActor = authority.Actor{RepositoryAdmin: true}
			in.Binding.ResolvedActor = authority.ResolvedActor{RepositoryAdmin: &authority.ResolvedRepositoryAdmin{RoleID: "999"}}
		},
	} {
		f, _ := authorityHTTPEnv(t)
		in := forgetest.WriteRestrictionInput()
		edit(&in)
		_, err := f.driver.ObserveWriteRestriction(t.Context(), in)
		if err == nil || f.calls["GET repos/acme/project"] != 0 {
			t.Fatal("invalid installation/App/role binding read remote state")
		}
	}
}

func TestAuthorityPartialAndMalformedHTTPResponses(t *testing.T) {
	for _, mode := range []string{"duplicate-id", "nonadvancing-page", "page-bound", "null-list", "wrong-detail-id", "changed-source", "unknown-enforcement", "missing-rules", "empty-rules", "invalid-conditions", "missing-conditions", "invalid-rules", "unknown-actor", "empty-actor-id", "unknown-parameters", "failed-second-detail", "primary-quota-page-two"} {
		t.Run(mode, func(t *testing.T) {
			f, e := authorityHTTPEnv(t)
			e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
			original := f.override
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
				if path == "repos/acme/project/rulesets" {
					summary := map[string]any{"id": 10, "source_type": "Repository", "source": "acme/project", "enforcement": "active"}
					switch mode {
					case "duplicate-id":
						f.json(w, []any{summary, summary})
						return true
					case "null-list":
						f.json(w, nil)
						return true
					case "nonadvancing-page", "page-bound", "primary-quota-page-two":
						page, _ := strconv.Atoi(r.URL.Query().Get("page"))
						if mode == "primary-quota-page-two" && page == 2 {
							w.Header().Set("X-RateLimit-Remaining", "0")
							w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(f.clock.Add(time.Hour).Unix(), 10))
							f.apiError(w, 403, "API rate limit exceeded")
							return true
						}
						next := 1
						if mode == "page-bound" {
							next = 11
						}
						if mode == "primary-quota-page-two" {
							next = 2
						}
						w.Header().Set("Link", fmt.Sprintf(`<%s%s?includes_parents=true&per_page=100&page=%d>; rel="next"`, f.server.URL, r.URL.Path, next))
						f.json(w, []any{summary})
						return true
					case "failed-second-detail":
						f.json(w, []any{summary, map[string]any{"id": 11, "source_type": "Organization", "source": "acme", "enforcement": "active"}})
						return true
					}
				}
				if path == "repos/acme/project/rulesets/11" {
					f.apiError(w, 404, "Not Found")
					return true
				}
				if path == "repos/acme/project/rulesets/10" {
					raw := map[string]any{"id": 10, "source_type": "Repository", "source": "acme/project", "target": "branch", "enforcement": "active", "updated_at": "2026-10-19T11:40:58.537-04:00", "conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}, "exclude": []string{}}}, "rules": []any{map[string]any{"type": "update"}}, "bypass_actors": []any{}}
					switch mode {
					case "wrong-detail-id":
						raw["id"] = 11
					case "changed-source":
						raw["source_type"] = "Organization"
						raw["source"] = "acme"
					case "unknown-enforcement":
						raw["enforcement"] = "something-new"
					case "missing-rules":
						delete(raw, "rules")
					case "empty-rules":
						raw["rules"] = []any{}
					case "invalid-rules":
						raw["rules"] = []any{map[string]any{"parameters": map[string]any{}}}
					case "missing-conditions":
						delete(raw, "conditions")
					case "invalid-conditions":
						raw["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}}}
					case "unknown-actor":
						raw["bypass_actors"] = []any{map[string]any{"actor_type": "User", "actor_id": 1, "bypass_mode": "always"}}
					case "empty-actor-id":
						raw["bypass_actors"] = []any{map[string]any{"actor_type": "Team", "bypass_mode": "always"}}
					case "unknown-parameters":
						raw["rules"] = []any{map[string]any{"type": "new_content_rule", "parameters": map[string]any{"new_param": map[string]any{"z": []any{1, "two", false}, "a": nil}}}}
					}
					f.json(w, raw)
					return true
				}
				return original(w, r)
			}
			o, err := f.driver.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
			must(t, o.Validate())
			wantErr := mode != "missing-rules" && mode != "empty-rules" && mode != "missing-conditions" && mode != "unknown-parameters"
			if (err != nil) != wantErr {
				t.Fatalf("error=%v observation=%+v", err, o)
			}
			if mode == "null-list" {
				if o.Inventory != authority.ReadFailure || len(o.Protections) != 0 {
					t.Fatalf("null list became empty inventory: %+v", o)
				}
				return
			}
			if len(o.Protections) == 0 {
				t.Fatal("successful partial evidence discarded")
			}
			p := o.Protections[0]
			switch mode {
			case "duplicate-id", "nonadvancing-page", "page-bound", "primary-quota-page-two":
				if o.Inventory != authority.ReadFailure || p.RulesVisibility != authority.Visible {
					t.Fatalf("partial inventory: %+v", o)
				}
				if mode == "primary-quota-page-two" {
					var quota *RateLimitError
					if !errors.As(err, &quota) || quota.Secondary {
						t.Fatalf("primary quota classified as permission denial: %v", err)
					}
				}
			case "wrong-detail-id", "changed-source", "unknown-enforcement":
				if p.RulesVisibility != authority.ReadFailure {
					t.Fatalf("mixed detail used: %+v", p)
				}
			case "missing-rules":
				if p.RulesVisibility != authority.Hidden {
					t.Fatal(p)
				}
			case "empty-rules":
				if p.RulesVisibility != authority.Visible || len(p.Rules) != 0 {
					t.Fatal(p)
				}
			case "missing-conditions":
				if p.TargetsVisibility != authority.Hidden {
					t.Fatal(p)
				}
			case "invalid-conditions":
				if p.TargetsVisibility != authority.ReadFailure || p.RulesVisibility != authority.Visible {
					t.Fatal(p)
				}
			case "invalid-rules":
				if p.RulesVisibility != authority.ReadFailure || p.TargetsVisibility != authority.Visible {
					t.Fatal(p)
				}
			case "unknown-actor", "empty-actor-id":
				if p.BypassVisibility != authority.ReadFailure || p.RulesVisibility != authority.Visible {
					t.Fatal(p)
				}
			case "unknown-parameters":
				if p.Rules[0].Parameters[0].Values[0] != `{"a":null,"z":[1,"two",false]}` {
					t.Fatal(p.Rules)
				}
			case "failed-second-detail":
				if len(o.Protections) != 2 || o.Protections[0].BypassVisibility != authority.Hidden || o.Protections[1].RulesVisibility != authority.Visible {
					t.Fatalf("lost independent inherited/repository evidence: %+v", o)
				}
			}
			if p.Revision != "" && p.Revision != "2026-10-19T15:40:58.537Z" {
				t.Fatalf("revision not normalized: %s", p.Revision)
			}
		})
	}
}
