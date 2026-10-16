package github

import (
	"bytes"
	"context"
	"encoding/json"
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
	_, err = planRules(ruleSnapshot{Base: "main", Rulesets: rules}, "acme/project", 42)
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
