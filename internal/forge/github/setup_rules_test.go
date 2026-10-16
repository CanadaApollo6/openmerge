package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func rulesAPI(t *testing.T) *apiFixture {
	t.Helper()
	f := newAPI(t)
	f.setupRules = make(map[int64]map[string]any)
	for _, rule := range fixtureRules(t) {
		data, err := json.Marshal(rule)
		must(t, err)
		var object map[string]any
		must(t, json.Unmarshal(data, &object))
		f.setupRules[rule.ID] = object
	}
	return f
}

func runRules(t *testing.T, f *apiFixture, apply bool) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := setupRules(context.Background(), RulesOptions{Repo: "acme/project", AppID: 42, Apply: apply}, "setup-admin-secret", &out, f.server.URL+"/api/v3/")
	return out.String(), err
}

func TestSetupRulesPreviewApplyIdempotence(t *testing.T) {
	f := rulesAPI(t)
	delete(f.setupRules, 11)
	base := f.setupRules[10]
	base["bypass_actors"] = []any{map[string]any{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "pull_request"}}
	originalRules, err := json.Marshal(base["rules"])
	must(t, err)
	originalConditions, err := json.Marshal(base["conditions"])
	must(t, err)
	out, err := runRules(t, f, false)
	if err == nil || !strings.Contains(out, "Preview only") || !strings.Contains(out, "+ ruleset openmerge-fence") || !strings.Contains(out, "base_bypass_missing") || f.setupWrites != 0 {
		t.Fatalf("preview = %s, %v, writes=%d", out, err, f.setupWrites)
	}
	out, err = runRules(t, f, true)
	must(t, err)
	if f.setupWrites != 2 || !strings.Contains(out, "Applied changes") {
		t.Fatalf("apply = %s, writes=%d", out, f.setupWrites)
	}
	actualRules, err := json.Marshal(base["rules"])
	must(t, err)
	actualConditions, err := json.Marshal(base["conditions"])
	must(t, err)
	if !bytes.Equal(originalRules, actualRules) || !bytes.Equal(originalConditions, actualConditions) || len(base["bypass_actors"].([]any)) != 2 {
		t.Fatal("base policy or existing bypass was changed")
	}
	out, err = runRules(t, f, true)
	must(t, err)
	if f.setupWrites != 2 || !strings.Contains(out, "No ruleset changes needed") {
		t.Fatalf("second apply = %s, writes=%d", out, f.setupWrites)
	}
}

func TestSetupRulesTightensOwnedFenceAndPreservesRules(t *testing.T) {
	f := rulesAPI(t)
	fence := f.setupRules[11]
	fence["enforcement"] = "disabled"
	fence["bypass_actors"] = []any{map[string]any{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}}
	fence["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{fencePrefix + "*"}, "exclude": []string{}}}
	fence["rules"] = []any{map[string]any{"type": "required_signatures"}, map[string]any{"type": "future_rule", "parameters": map[string]any{"keep": "all values"}}}
	out, err := runRules(t, f, true)
	must(t, err)
	if f.setupWrites != 1 || !strings.Contains(out, "- bypass_actors=") || !strings.Contains(out, "+ rule deletion") {
		t.Fatalf("update = %s", out)
	}
	rules := fence["rules"].([]any)
	if len(rules) != 6 || rules[1].(map[string]any)["parameters"].(map[string]any)["keep"] != "all values" {
		t.Fatalf("lost existing fence rules: %v", rules)
	}
	_, err = runRules(t, f, true)
	must(t, err)
	if f.setupWrites != 1 {
		t.Fatal("owned fence update is not idempotent")
	}
}

func TestSetupRulesReportsInheritedAndQueueBlockers(t *testing.T) {
	f := rulesAPI(t)
	base := f.setupRules[10]
	base["source_type"], base["source"] = "Organization", "acme"
	base["bypass_actors"] = []any{}
	queue := map[string]any{}
	for k, v := range base {
		queue[k] = v
	}
	queue["id"], queue["name"] = 12, "organization-queue-policy"
	queue["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{queuePrefix + "main/*"}, "exclude": []string{}}}
	f.setupRules[12] = queue
	out, err := runRules(t, f, true)
	if err == nil || !strings.Contains(out, "base_bypass_missing") || !strings.Contains(out, "ref_blocked") || f.setupWrites != 0 {
		t.Fatalf("blockers = %s %v", out, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls["GET repos/acme/project/rulesets"] < 2 || f.calls["GET repos/acme/project/rulesets/12"] == 0 {
		t.Fatal("did not paginate and fetch detailed inherited policies")
	}
}

func TestSetupRulesRefusesUnsafeFenceShape(t *testing.T) {
	for _, conditions := range []string{
		`{"ref_name":{"include":["~ALL"],"exclude":[]}}`,
		`{"ref_name":{"include":["refs/heads/openmerge/fence/**/*"],"exclude":["refs/heads/openmerge/fence/main"]}}`,
	} {
		f := rulesAPI(t)
		var value any
		must(t, json.Unmarshal([]byte(conditions), &value))
		f.setupRules[11]["conditions"] = value
		_, err := runRules(t, f, true)
		if err == nil || f.setupWrites != 0 {
			t.Fatalf("unsafe policy was edited: %v", err)
		}
	}
}

func TestSetupRulesFailsClosed(t *testing.T) {
	for _, kind := range []string{"hidden bypass", "not admin", "permission denied", "concurrent change", "write failure"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			delete(f.setupRules, 11)
			switch kind {
			case "hidden bypass":
				delete(f.setupRules[10], "bypass_actors")
			case "not admin":
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/api/v3/repos/acme/project" {
						f.json(w, map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": false}})
						return true
					}
					return false
				}
			case "permission denied", "write failure":
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if kind == "permission denied" || r.Method == "POST" {
						f.apiError(w, 403, "setup-admin-secret")
						return true
					}
					return false
				}
			case "concurrent change":
				f.override = func(_ http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/api/v3/repos/acme/project" && f.calls["GET repos/acme/project"] == 2 {
						f.setupRules[10]["name"] = "changed-during-preview"
					}
					return false
				}
			}
			_, err := runRules(t, f, true)
			if err == nil || f.setupWrites != 0 || strings.Contains(err.Error(), "setup-admin-secret") {
				t.Fatalf("unsafe apply: %v, writes=%d", err, f.setupWrites)
			}
		})
	}
}

func TestSetupRulesOutputFailurePreventsWrites(t *testing.T) {
	f := rulesAPI(t)
	delete(f.setupRules, 11)
	err := setupRules(context.Background(), RulesOptions{Repo: "acme/project", AppID: 42, Apply: true}, "setup-admin-secret", failingOutput{}, f.server.URL+"/api/v3/")
	if err == nil || f.setupWrites != 0 {
		t.Fatalf("write after failed preview: %v", err)
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPlanRulesPreservesOriginalSnapshot(t *testing.T) {
	rules := fixtureRules(t)
	rules[0].BypassActors = rules[0].BypassActors[:1]
	rules[0].BypassActors[0].BypassMode = "pull_request"
	before, err := json.Marshal(rules)
	must(t, err)
	_, err = planRules(ruleSnapshot{DefaultBranch: "main", Rulesets: rules}, "acme/project", 42)
	must(t, err)
	after, err := json.Marshal(rules)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("planner mutated input")
	}
}

func TestSetupRulesUpgradesAppWithoutDuplicateActor(t *testing.T) {
	f := rulesAPI(t)
	actors := f.setupRules[10]["bypass_actors"].([]any)
	actors[0].(map[string]any)["bypass_mode"] = "pull_request"
	_, err := runRules(t, f, true)
	must(t, err)
	actors = f.setupRules[10]["bypass_actors"].([]any)
	if len(actors) != 2 || actors[0].(map[string]any)["bypass_mode"] != "always" {
		t.Fatalf("duplicated App actor: %v", actors)
	}
}

func TestSetupRulesDisallowsFenceUpstreamUpdates(t *testing.T) {
	f := rulesAPI(t)
	rules := f.setupRules[11]["rules"].([]any)
	update := rules[1].(map[string]any)
	update["parameters"] = map[string]any{"update_allows_fetch_and_merge": true, "future_parameter": "preserve"}
	out, err := runRules(t, f, true)
	must(t, err)
	rules = f.setupRules[11]["rules"].([]any)
	params := rules[1].(map[string]any)["parameters"].(map[string]any)
	if params["update_allows_fetch_and_merge"] != false || params["future_parameter"] != "preserve" || !strings.Contains(out, "+ update.update_allows_fetch_and_merge=false") {
		t.Fatalf("fence update parameters lost or unsafe: %v, %s", params, out)
	}
}

func TestSetupRulesIntegrationBypassRejection(t *testing.T) {
	for _, tt := range []struct {
		name     string
		status   int
		message  string
		details  []any
		guide    bool
		personal bool
	}{
		{"conflict", 409, "Integration bypass actor conflicts with repository", nil, true, false},
		{"actor field", 422, "Validation failed", []any{map[string]any{"resource": "RepositoryRuleset", "field": "bypass_actors[0].actor_id", "code": "invalid"}}, true, false},
		{"actor resource", 422, "Validation failed", []any{map[string]any{"resource": "bypass_actors", "field": "actor_id", "code": "invalid"}}, true, false},
		{"actor detail string", 422, "Validation failed", []any{"Integration is not a valid bypass actor type"}, true, false},
		{"unrelated validation", 422, "Validation failed", []any{map[string]any{"field": "conditions", "code": "invalid"}}, false, false},
		{"unrelated conflict", 409, "Repository rules changed", nil, false, true},
		{"permission denied", 403, "Resource not accessible by integration", nil, false, true},
		{"personal bare conflict", 409, "Conflict", nil, false, true},
		{"personal conditions conflict", 409, "Conflict", []any{map[string]any{"field": "conditions", "code": "invalid"}}, false, true},
		{"integration tests check", 422, "Validation failed: integration-tests is not a valid check", nil, false, true},
		{"integration id check", 422, "Validation failed", []any{map[string]any{"field": "rules.parameters.required_status_checks", "message": "integration_id is not a valid check name"}}, false, true},
		{"organization bare conflict", 409, "Conflict", nil, false, false},
	} {
		for _, update := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/update=%v", tt.name, update), func(t *testing.T) {
				f := rulesAPI(t)
				if update {
					f.setupRules[10]["bypass_actors"] = []any{}
				} else {
					delete(f.setupRules, 11)
				}
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if tt.personal && r.URL.Path == "/api/v3/repos/acme/project" {
						f.json(w, map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": true}, "owner": map[string]string{"type": "User"}})
						return true
					}
					if r.Method != http.MethodPost && r.Method != http.MethodPut {
						return false
					}
					w.WriteHeader(tt.status)
					f.json(w, map[string]any{"message": tt.message, "credential": "setup-admin-secret", "errors": tt.details, "documentation_url": "https://docs.github.com/en/rest/repos/rules"})
					return true
				}
				_, err := runRules(t, f, true)
				var api *APIError
				if !errors.As(err, &api) || api.StatusCode != tt.status || strings.Contains(err.Error(), "setup-admin-secret") || f.setupWrites != 0 {
					t.Fatalf("unsafe apply failure: %v; writes=%d", err, f.setupWrites)
				}
				for _, guidance := range []string{"Install App 42 on acme/project first", "personal-account repository", "Keep protections enabled", "organization-owned repository", "defer OpenMerge"} {
					if strings.Contains(err.Error(), guidance) != tt.guide {
						t.Errorf("guidance %q expected=%v: %v", guidance, tt.guide, err)
					}
				}
				if !strings.Contains(err.Error(), tt.message) {
					t.Errorf("lost GitHub diagnostic: %v", err)
				}
				wantHint := !tt.guide && (tt.status == 409 || tt.status == 422)
				if strings.Contains(err.Error(), "If this concerns bypass actors, verify the App installation and repository support") != wantHint {
					t.Errorf("conditional hint expected=%v: %v", wantHint, err)
				}
			})
		}
	}
}

func TestSetupRulesSanitizesErrorContext(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOjF9.c2lnbmF0dXJl"
	for _, status := range []int{403, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := rulesAPI(t)
			f.setupRules[10]["name"] = "protected setup-admin-secret " + jwt + " -----BEGIN PRIVATE KEY-----\nprivate-key-material\n-----END PRIVATE KEY-----"
			f.setupRules[10]["bypass_actors"] = []any{}
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPut {
					return false
				}
				w.WriteHeader(status)
				f.json(w, map[string]string{"message": "bypass actors rejected setup-admin-secret", "documentation_url": "https://docs.github.com/en/rest/repos/rules"})
				return true
			}
			_, err := runRules(t, f, true)
			var api *APIError
			if !errors.As(err, &api) || api.StatusCode != status {
				t.Fatalf("lost typed status: %v", err)
			}
			for _, secret := range []string{"setup-admin-secret", jwt, "private-key-material", "BEGIN PRIVATE KEY"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error context leaked %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "protected [REDACTED]") || !strings.Contains(err.Error(), "documentation_url=https://docs.github.com/en/rest/repos/rules") {
				t.Errorf("lost useful context: %v", err)
			}
		})
	}
}

func TestSetupRulesUsesRepositoryDefaultBranch(t *testing.T) {
	f := rulesAPI(t)
	f.setupRules[10]["conditions"] = map[string]any{"ref_name": map[string]any{"include": []string{"~DEFAULT_BRANCH"}, "exclude": []string{}}}
	f.setupRules[10]["bypass_actors"] = []any{}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v3/repos/acme/project" {
			return false
		}
		f.json(w, map[string]any{"default_branch": "release/v1", "permissions": map[string]bool{"admin": true}})
		return true
	}
	out, err := runRules(t, f, true)
	must(t, err)
	if !strings.Contains(out, "base release/v1") || !strings.Contains(out, "base_bypass_missing") || f.setupWrites != 1 {
		t.Fatalf("did not validate and repair the repository default branch: %s; writes=%d", out, f.setupWrites)
	}
}
