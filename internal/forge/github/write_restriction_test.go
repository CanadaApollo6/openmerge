package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestAuthorityRepositorySourceCaseAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, listSource, detailSource string
		repositoryID                   int64
		wantErr                        bool
	}{
		{"mixed-case", "Acme/Project", "Acme/Project", 99, false},
		{"list-detail-case", "acme/project", "Acme/Project", 99, false},
		{"different-repository", "acme/other", "acme/other", 99, true},
		{"different-id", "Acme/Project", "Acme/Project", 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := authorityHTTPEnv(t)
			e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
			original := f.override
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				switch strings.TrimPrefix(r.URL.Path, "/api/v3/") {
				case "repos/acme/project":
					f.json(w, map[string]any{"id": tc.repositoryID, "default_branch": "main"})
					return true
				case "repos/acme/project/rulesets":
					f.json(w, []any{map[string]any{"id": 10, "source_type": "Repository", "source": tc.listSource, "target": "branch", "enforcement": "active"}})
					return true
				case "repos/acme/project/rulesets/10":
					f.json(w, map[string]any{"id": 10, "source_type": "Repository", "source": tc.detailSource, "target": "branch", "enforcement": "active", "conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}, "exclude": []string{}}}, "rules": []any{}, "bypass_actors": []any{}})
					return true
				}
				return original(w, r)
			}
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
			must(t, o.Validate())
			if (err != nil) != tc.wantErr {
				t.Fatalf("source/identity check: %+v %v", o, err)
			}
			if tc.wantErr {
				if len(o.Protections) != 0 || o.Inventory != authority.ReadFailure {
					t.Fatalf("mismatched repository trusted: %+v", o)
				}
			} else if len(o.Protections) != 1 || o.Protections[0].BypassVisibility != authority.Visible {
				t.Fatalf("case-only change lost protection: %+v", o)
			}
		})
	}
}

func TestAuthorityParameterLeafTypesHaveDistinctFingerprints(t *testing.T) {
	seen := map[string]string{}
	for _, tc := range []struct {
		name, raw string
		want      []authority.Parameter
	}{
		{"null", `{"leaf":null}`, []authority.Parameter{{Name: "leaf", Values: []string{"null"}}}},
		{"empty-array", `{"leaf":[]}`, []authority.Parameter{{Name: "leaf", Values: []string{}}}},
		{"absent", `{}`, []authority.Parameter{}},
		{"null-string", `{"leaf":"null"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"null"`}}}},
		{"empty-object", `{"leaf":{}}`, []authority.Parameter{{Name: "leaf", Values: []string{"{}"}}}},
		{"object-string", `{"leaf":"{}"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"{}"`}}}},
		{"boolean", `{"leaf":true}`, []authority.Parameter{{Name: "leaf", Values: []string{"true"}}}},
		{"boolean-string", `{"leaf":"true"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"true"`}}}},
		{"number", `{"leaf":42}`, []authority.Parameter{{Name: "leaf", Values: []string{"42"}}}},
		{"number-string", `{"leaf":"42"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"42"`}}}},
		{"string", `{"leaf":"a"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"a"`}}}},
		{"string-array", `{"leaf":["a"]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{`"a"`}}}},
		{"number-array", `{"leaf":[42]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{"42"}}}},
		{"null-array", `{"leaf":[null]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{"null"}}}},
		{"boolean-array", `{"leaf":[true]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{"true"}}}},
		{"empty-string", `{"leaf":""}`, []authority.Parameter{{Name: "leaf", Values: []string{`""`}}}},
		{"empty-string-array", `{"leaf":[""]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{`""`}}}},
		{"numeric-key", `{"leaf":{"0":42}}`, []authority.Parameter{{Name: "leaf/0", Values: []string{"42"}}}},
		{"nested-array", `{"leaf":[[42]]}`, []authority.Parameter{{Name: "leaf/~20/~20", Values: []string{"42"}}}},
		{"arrays-of-objects", `{"leaf":[[{"0":42}]]}`, []authority.Parameter{{Name: "leaf/~20/~20/0", Values: []string{"42"}}}},
		{"objects-of-arrays", `{"leaf":{"0":[[42]]}}`, []authority.Parameter{{Name: "leaf/0/~20/~20", Values: []string{"42"}}}},
		{"json-string", `{"leaf":"{\"0\":42}"}`, []authority.Parameter{{Name: "leaf", Values: []string{`"{\"0\":42}"`}}}},
		{"reserved-array-marker-key", `{"leaf":{"~20":42}}`, []authority.Parameter{{Name: "leaf/~020", Values: []string{"42"}}}},
		{"empty-key", `{"leaf":{"":42}}`, []authority.Parameter{{Name: "leaf/~3", Values: []string{"42"}}}},
		{"reserved-empty-key-marker", `{"leaf":{"~3":42}}`, []authority.Parameter{{Name: "leaf/~03", Values: []string{"42"}}}},
		{"top-level-empty-key", `{"":42}`, []authority.Parameter{{Name: "~3", Values: []string{"42"}}}},
		{"nested-empty-array", `{"leaf":[[]]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{}}}},
		{"array-of-empty-object", `{"leaf":[{}]}`, []authority.Parameter{{Name: "leaf/~20", Values: []string{"{}"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := (authorityRuleset{ID: 10, SourceType: "Repository", Source: "acme/project", Target: "branch", Enforcement: "active", Conditions: json.RawMessage(`{"ref_name":{"include":["refs/heads/main"],"exclude":[]}}`), Rules: json.RawMessage(`[{"type":"future_rule","parameters":` + tc.raw + `}]`), Bypass: json.RawMessage(`[]`)}).protection("acme/project")
			must(t, err)
			if !reflect.DeepEqual(p.Rules[0].Parameters, tc.want) {
				t.Fatalf("parameters=%+v, want %+v", p.Rules[0].Parameters, tc.want)
			}
			o := forge.EmptyWriteRestrictionObservation()
			o.Inventory, o.Protections = authority.Visible, []authority.Protection{p}
			o.Coverage = []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/10"}}}
			fingerprint, err := o.Fingerprint()
			must(t, err)
			if previous, ok := seen[fingerprint]; ok {
				t.Fatalf("%s and %s share fingerprint %s", previous, tc.name, fingerprint)
			}
			seen[fingerprint] = tc.name
		})
	}
}

// Pair each generated value with several container shapes to exercise type and
// path boundaries even when the random generator repeats a common scalar.
func TestAuthorityParameterEncodingInjective(t *testing.T) {
	rng := rand.New(rand.NewPCG(98, 3))
	keys := []string{"", "0", "1", "a", "a/b", "~", "~20", "~3", "雪", "\x00"}
	var value func(int) any
	value = func(depth int) any {
		kind := rng.IntN(6)
		if depth == 0 {
			kind = rng.IntN(4)
		}
		switch kind {
		case 0:
			return nil
		case 1:
			return rng.IntN(2) == 0
		case 2:
			return rng.IntN(200) - 100
		case 3:
			return []string{"", "a", "null", "42", "true", "{}", "[]", `{"0":[42]}`, "a/b~c", "雪", "\x00"}[rng.IntN(11)]
		case 4:
			items := make([]any, rng.IntN(4))
			for i := range items {
				items[i] = value(depth - 1)
			}
			return items
		default:
			object := map[string]any{}
			for range rng.IntN(4) {
				object[keys[rng.IntN(len(keys))]] = value(depth - 1)
			}
			return object
		}
	}
	seen := map[string]string{}
	const samples = 2000
	for range samples {
		x := value(4)
		jsonString, err := json.Marshal(x)
		must(t, err)
		for _, shape := range []any{x, []any{x}, []any{[]any{x}}, map[string]any{"0": x}, map[string]any{"": x}, map[string]any{"~20": x}, map[string]any{"~3": x}, string(jsonString)} {
			raw, err := json.Marshal(map[string]any{"leaf": shape})
			must(t, err)
			var decoded any
			must(t, json.Unmarshal(raw, &decoded))
			canonical, err := json.Marshal(decoded)
			must(t, err)
			parameters, err := authorityParameters(canonical)
			must(t, err)
			encoded, err := json.Marshal(parameters)
			must(t, err)
			if previous, ok := seen[string(encoded)]; ok && previous != string(canonical) {
				t.Fatalf("encoding collision: %s and %s => %s", previous, canonical, encoded)
			}
			seen[string(encoded)] = string(canonical)
		}
	}
	t.Logf("%d generated inputs, %d distinct canonical JSON values, no collisions (PCG seeds 98, 3)", samples*8, len(seen))
}

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
		summary := map[string]any{"id": 10, "source_type": sourceType, "source": source, "target": "branch", "enforcement": "active"}
		if strings.HasPrefix(scenario, "target-") {
			summary["target"] = strings.TrimPrefix(scenario, "target-")
		}
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
		summary["updated_at"] = "2026-10-20T00:00:00Z"
		summary["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main", "refs/heads/release/**/*"}, "exclude": []string{}}}
		if scenario == "target-tag" {
			summary["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{"refs/tags/v*"}, "exclude": []string{}}}
		}
		if scenario == "target-push" || scenario == "target-repository" {
			summary["conditions"] = map[string]any{}
		}
		summary["rules"] = []any{map[string]any{"type": "update", "parameters": map[string]any{"update_allows_fetch_and_merge": false}}}
		switch scenario {
		case "hidden-bypass", "inherited-hidden-bypass":
		case "null-bypass":
			summary["bypass_actors"] = nil
		case "unauthorized-actors":
			summary["bypass_actors"] = []any{
				map[string]any{"actor_type": "User", "actor_id": 91, "bypass_mode": "always"},
				map[string]any{"actor_type": "DeployKey", "actor_id": nil, "bypass_mode": "always"},
				map[string]any{"actor_type": "OrganizationAdmin", "bypass_mode": "exempt"},
				map[string]any{"actor_type": "Team", "actor_id": 92, "bypass_mode": "exempt"},
			}
		case "class-wide-actors", "class-wide-actors-with-id":
			actors := []any{}
			for _, kind := range []string{"OrganizationAdmin", "DeployKey", "EnterpriseOwner"} {
				a := map[string]any{"actor_type": kind, "bypass_mode": "always"}
				if scenario == "class-wide-actors-with-id" {
					a["actor_id"] = 95
				}
				actors = append(actors, a)
			}
			summary["bypass_actors"] = actors
		case "unknown-actor":
			summary["bypass_actors"] = []any{
				map[string]any{"actor_type": "FutureActor", "bypass_mode": "pull_request"},
				map[string]any{"actor_type": "FutureActor", "actor_id": 93, "bypass_mode": "exempt"},
			}
		case "visible-empty-bypass":
			summary["bypass_actors"] = []any{}
		default:
			summary["bypass_actors"] = []any{
				map[string]any{"actor_type": "Integration", "actor_id": 42, "bypass_mode": "always"},
				map[string]any{"actor_type": "Team", "actor_id": 44, "bypass_mode": "always"},
				map[string]any{"actor_type": "RepositoryRole", "actor_id": 5, "bypass_mode": "pull_request"},
			}
		}
		if strings.HasPrefix(scenario, "overflow-") {
			fillAuthorityCollection(summary, strings.TrimPrefix(scenario, "overflow-"), 1001)
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
	for _, mode := range []string{"duplicate-id", "nonadvancing-page", "page-bound", "null-list", "wrong-detail-id", "changed-source", "unknown-enforcement", "missing-rules", "empty-rules", "invalid-conditions", "missing-conditions", "invalid-rules", "missing-actor-kind", "empty-actor-id", "zero-unknown-actor-id", "negative-unknown-actor-id", "unknown-parameters", "failed-second-detail", "primary-quota-page-two"} {
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
					case "missing-actor-kind":
						raw["bypass_actors"] = []any{map[string]any{"actor_id": 1, "bypass_mode": "always"}}
					case "empty-actor-id":
						raw["bypass_actors"] = []any{map[string]any{"actor_type": "Team", "bypass_mode": "always"}}
					case "zero-unknown-actor-id", "negative-unknown-actor-id":
						id := 0
						if mode == "negative-unknown-actor-id" {
							id = -1
						}
						raw["bypass_actors"] = []any{map[string]any{"actor_type": "FutureActor", "actor_id": id, "bypass_mode": "always"}}
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
			case "missing-actor-kind", "empty-actor-id", "zero-unknown-actor-id", "negative-unknown-actor-id":
				if p.BypassVisibility != authority.ReadFailure || p.RulesVisibility != authority.Visible {
					t.Fatal(p)
				}
			case "unknown-parameters":
				if !reflect.DeepEqual(p.Rules[0].Parameters, []authority.Parameter{{Name: "new_param/a", Values: []string{"null"}}, {Name: "new_param/z/~20", Values: []string{"1"}}, {Name: "new_param/z/~21", Values: []string{`"two"`}}, {Name: "new_param/z/~22", Values: []string{"false"}}}) {
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

// Each fixture fills one independently bounded evidence collection.
func fillAuthorityCollection(raw map[string]any, field string, n int) {
	values := make([]string, n)
	params := make(map[string]any, n)
	rules := make([]any, n)
	actors := make([]any, n)
	for i := range n {
		values[i] = "refs/heads/branch-" + strconv.Itoa(i)
		params["param-"+strconv.Itoa(i)] = true
		rules[i] = map[string]any{"type": "creation"}
		actors[i] = map[string]any{"actor_type": "User", "actor_id": i + 1, "bypass_mode": "always"}
	}
	conditions := raw["conditions"].(map[string]any)
	switch field {
	case "includes":
		conditions["ref_name"].(map[string]any)["include"] = values
	case "excludes":
		conditions["ref_name"].(map[string]any)["exclude"] = values
	case "conditions":
		conditions["repository_property"] = params
	case "condition-values":
		conditions["repository_id"] = map[string]any{"repository_ids": values}
	case "rules":
		raw["rules"] = rules
	case "rule-parameters":
		raw["rules"] = []any{map[string]any{"type": "new_rule", "parameters": params}}
	case "rule-values":
		raw["rules"] = []any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{"contexts": values}}}
	case "bypass":
		raw["bypass_actors"] = actors
	}
}

func TestAuthorityCollectionBoundary(t *testing.T) {
	for _, field := range []string{"includes", "excludes", "conditions", "condition-values", "rules", "rule-parameters", "rule-values", "bypass"} {
		t.Run(field, func(t *testing.T) {
			f, e := authorityHTTPEnv(t)
			e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
			original := f.override
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/rulesets/10") {
					raw := map[string]any{"id": 10, "source_type": "Repository", "source": "acme/project", "target": "branch", "enforcement": "active", "conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/main"}, "exclude": []string{}}}, "rules": []any{}, "bypass_actors": []any{}}
					fillAuthorityCollection(raw, field, 1000)
					f.json(w, raw)
					return true
				}
				return original(w, r)
			}
			o, err := e.Reader.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
			must(t, err)
			must(t, o.Validate())
			p := o.Protections[0]
			if p.TargetsVisibility != authority.Visible || p.RulesVisibility != authority.Visible || p.BypassVisibility != authority.Visible {
				t.Fatalf("valid boundary hidden: %+v", p)
			}
			n := 0
			switch field {
			case "includes":
				n = len(p.Includes)
			case "excludes":
				n = len(p.Excludes)
			case "conditions":
				n = len(p.Conditions)
			case "condition-values":
				n = len(p.Conditions)
			case "rules":
				n = len(p.Rules)
			case "rule-parameters":
				n = len(p.Rules[0].Parameters)
			case "rule-values":
				n = len(p.Rules[0].Parameters)
			case "bypass":
				n = len(p.BypassActors)
			}
			if n != 1000 {
				t.Fatalf("collection truncated: %d", n)
			}
		})
	}
}

func TestAuthorityInheritedConditionAndNestedRulePaths(t *testing.T) {
	f, e := authorityHTTPEnv(t)
	e.Configure("inherited")
	e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
	original := f.override
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/rulesets/10") {
			f.json(w, map[string]any{
				"id": 10, "source_type": "Organization", "source": "acme", "target": "branch", "enforcement": "active",
				"conditions": map[string]any{
					"ref_name":            map[string]any{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}},
					"repository_name":     map[string]any{"include": []string{"project"}, "exclude": []string{}, "protected": true},
					"repository_id":       map[string]any{"repository_ids": []int{99, 100}},
					"repository_property": map[string]any{"include": []any{map[string]any{"name": "tier", "source": "custom", "property_values": []string{"critical", "high"}}}, "exclude": []any{}},
				},
				"rules": []any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{
					"required_status_checks":               []any{map[string]any{"context": "ci/a", "integration_id": 42}, map[string]any{"context": "ci/b", "integration_id": nil}},
					"strict_required_status_checks_policy": false,
					"future/key~":                          map[string]any{"values": []any{1, "two", false}},
				}}},
				"bypass_actors": []any{
					map[string]any{"actor_type": "DeployKey", "actor_id": 94, "bypass_mode": "pull_request"},
					map[string]any{"actor_type": "OrganizationAdmin", "actor_id": 95, "bypass_mode": "always"},
					map[string]any{"actor_type": "EnterpriseRole", "actor_id": 96, "bypass_mode": "exempt"},
					map[string]any{"actor_type": "EnterpriseOwner", "bypass_mode": "always"},
				},
			})
			return true
		}
		return original(w, r)
	}
	o, err := e.Reader.ObserveWriteRestriction(t.Context(), forgetest.WriteRestrictionInput())
	must(t, err)
	must(t, o.Validate())
	p := o.Protections[0]
	wantConditions := []authority.Parameter{
		{Name: "repository_id/repository_ids/~20", Values: []string{"99"}},
		{Name: "repository_id/repository_ids/~21", Values: []string{"100"}},
		{Name: "repository_name/exclude", Values: []string{}},
		{Name: "repository_name/include/~20", Values: []string{`"project"`}},
		{Name: "repository_name/protected", Values: []string{"true"}},
		{Name: "repository_property/exclude", Values: []string{}},
		{Name: "repository_property/include/~20/name", Values: []string{`"tier"`}},
		{Name: "repository_property/include/~20/property_values/~20", Values: []string{`"critical"`}},
		{Name: "repository_property/include/~20/property_values/~21", Values: []string{`"high"`}},
		{Name: "repository_property/include/~20/source", Values: []string{`"custom"`}},
	}
	wantRules := []authority.Rule{{Kind: "required_status_checks", Parameters: []authority.Parameter{
		{Name: "future~1key~0/values/~20", Values: []string{"1"}},
		{Name: "future~1key~0/values/~21", Values: []string{`"two"`}},
		{Name: "future~1key~0/values/~22", Values: []string{"false"}},
		{Name: "required_status_checks/~20/context", Values: []string{`"ci/a"`}},
		{Name: "required_status_checks/~20/integration_id", Values: []string{"42"}},
		{Name: "required_status_checks/~21/context", Values: []string{`"ci/b"`}},
		{Name: "required_status_checks/~21/integration_id", Values: []string{"null"}},
		{Name: "strict_required_status_checks_policy", Values: []string{"false"}},
	}}}
	wantActors := []authority.BypassActor{{Kind: "deploy_key", Mode: "pull_request"}, {Kind: "organization_admin", Mode: "always"}, {Kind: "enterprise_role", ID: "96", Mode: "exempt"}, {Kind: "enterprise_owner", Mode: "always"}}
	if !reflect.DeepEqual(p.Conditions, wantConditions) || !reflect.DeepEqual(p.Rules, wantRules) || !reflect.DeepEqual(p.BypassActors, wantActors) || !reflect.DeepEqual(p.Includes, []string{"~DEFAULT_BRANCH"}) || p.Target != "branch" {
		t.Fatalf("lost inherited or nested evidence: %+v", p)
	}
}
