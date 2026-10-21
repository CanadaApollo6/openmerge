package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

func rollbackOptions(t *testing.T, f *apiFixture) RulesOptions {
	t.Helper()
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	opts.Authority = "github"
	opts.RollbackPreflight = true
	opts.Apply = false
	opts.Backup = ""
	opts.RuntimeDriver = f.driver
	must(t, os.WriteFile(opts.Config, []byte(strings.Replace(strings.Replace(setupCandidate, "policy_authority: openmerge\n", "", 1), "break_glass: {team: acme/owners}\n", "", 1)+"- name: release\n  base: release/**\n  merge_method: merge\n  merge_when: [check-success = ci]\n"), 0600))
	return opts
}

func rollbackReads(t *testing.T, f *apiFixture, classic map[string]any, signatures any) func(http.ResponseWriter, *http.Request) bool {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/branches") {
			f.json(w, paginate(f, r, w, []map[string]any{{"name": "main"}, {"name": "release/2026/oct"}, {"name": "unselected"}, {"name": "openmerge/verify/main"}}))
			return true
		}
		if strings.Contains(r.URL.Path, "/protection") {
			if r.Header.Get("Authorization") != "Bearer setup-admin-secret" {
				t.Error("classic preflight used installation credentials")
			}
			if classic == nil {
				f.apiError(w, 404, "Branch not protected")
				return true
			}
			if strings.HasSuffix(r.URL.Path, "/required_signatures") {
				f.json(w, signatures)
				return true
			}
			f.json(w, classic)
			return true
		}
		return false
	}
}

func TestRollbackPreflightPureLegacyValidatorsAndAdminClassic(t *testing.T) {
	f := rulesAPI(t)
	opts := rollbackOptions(t, f)
	classic := completeClassicFixture()
	classic["required_pull_request_reviews"] = map[string]any{"bypass_pull_request_allowances": map[string]any{"apps": []map[string]any{{"id": 42}}}}
	classic["required_linear_history"] = map[string]any{"enabled": true}
	f.override = rollbackReads(t, f, classic, map[string]any{"enabled": false})
	out, err := runAuthority(t, f, opts)
	// Squash main and all fences are compatible with linear history, while the
	// selected merge-method release branch fails. A leased FF needs no force.
	if err == nil || !strings.Contains(out, "classic_linear_history") || !strings.Contains(out, "release/2026/oct") || strings.Contains(out, "classic_force_push") || strings.Contains(out, "classic_reviews") || strings.Contains(out, "ref=\"openmerge/fence/main\" code=legacy_incompatible") {
		t.Fatalf("wrong classic semantics: %s %v", out, err)
	}
	if f.setupWrites != 2 {
		t.Fatal("preflight mutated rules")
	}
	for _, ref := range []string{"main", "release/2026/oct", "openmerge/fence/main", "openmerge/fence/release/2026/oct"} {
		if f.calls["GET repos/acme/project/branches/"+ref+"/protection"] == 0 || f.calls["GET repos/acme/project/branches/"+ref+"/protection/required_signatures"] == 0 {
			t.Fatal("incomplete admin classic read", ref)
		}
	}
	classic["required_linear_history"] = map[string]any{"enabled": false}
	out, err = runAuthority(t, f, opts)
	must(t, err)
	if !strings.Contains(out, "Rollback preflight passed") || f.setupWrites != 2 {
		t.Fatal(out)
	}
	// Explicitly unprotected is also compatible; generic 404 never is.
	f.override = rollbackReads(t, f, nil, nil)
	_, err = runAuthority(t, f, opts)
	must(t, err)
}

func TestRollbackReportsAllBlockersAndOperatorChoices(t *testing.T) {
	f := rulesAPI(t)
	opts := rollbackOptions(t, f)
	f.setupRules[10]["bypass_actors"] = []any{}
	f.setupRules[10]["source_type"], f.setupRules[10]["source"] = "Organization", "acme"
	f.setupRules[10]["rules"] = []map[string]any{{"type": "required_linear_history"}} // compatible content still needs legacy bypass
	inherited := map[string]any{"id": 12, "name": "org-green", "source_type": "Organization", "source": "acme", "enforcement": "active", "target": "branch", "bypass_actors": []any{}, "conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/openmerge/green/**/*"}, "exclude": []string{}}}, "rules": []map[string]any{{"type": "required_signatures"}}}
	f.setupRules[12] = inherited
	classic := completeClassicFixture()
	classic["required_signatures"] = map[string]any{"enabled": true}
	reads := rollbackReads(t, f, classic, nil)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			token := f.object("installation_token")
			token["permissions"] = map[string]string{}
			f.json(w, token)
			return true
		}
		return reads(w, r)
	}
	out, err := runAuthority(t, f, opts)
	for _, expected := range []string{"BLOCKER source=rulesets", "ref=\"refs/heads/main\"", "base_bypass_missing", "refs/heads/openmerge/green/*", "Operator choices:", "loses App content enforcement", "inherited-policy owner", "stay in authority mode", "classic_signed_commits", "BLOCKER source=permissions", "contents: write", "workflows: write"} {
		if !strings.Contains(out, expected) {
			t.Errorf("missing %q: %s %v", expected, out, err)
		}
	}
	if err == nil || f.setupWrites != 2 {
		t.Fatal("false rollback certification", err)
	}
}

func TestRollbackFailsIncompleteReadsAndMissingInstallation(t *testing.T) {
	for _, kind := range []string{"generic404", "protection500", "null protection", "empty protection", "signature500", "signature missing", "embedded signature missing", "inventory500", "nil inventory", "missing installation", "rules500"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			opts := rollbackOptions(t, f)
			classic := completeClassicFixture()
			if kind == "rules500" {
				classic["required_signatures"] = map[string]any{"enabled": true}
			}
			reads := rollbackReads(t, f, classic, map[string]any{"enabled": false})
			if kind == "missing installation" {
				opts.RuntimeDriver = nil
			}
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := r.URL.Path
				switch {
				case kind == "rules500" && strings.HasSuffix(path, "/rulesets/11"):
					f.apiError(w, 500, "injected")
					return true
				case kind == "generic404" && strings.HasSuffix(path, "/protection"):
					f.apiError(w, 404, "Not Found")
					return true
				case kind == "protection500" && strings.HasSuffix(path, "/protection"):
					f.apiError(w, 500, "injected")
					return true
				case kind == "null protection" && strings.HasSuffix(path, "/protection"):
					f.json(w, nil)
					return true
				case kind == "empty protection" && strings.HasSuffix(path, "/protection"):
					f.json(w, map[string]any{})
					return true
				case kind == "embedded signature missing" && strings.HasSuffix(path, "/protection"):
					f.json(w, map[string]any{"required_signatures": map[string]any{}})
					return true
				case kind == "signature500" && strings.HasSuffix(path, "/required_signatures"):
					f.apiError(w, 500, "injected")
					return true
				case kind == "signature missing" && strings.HasSuffix(path, "/required_signatures"):
					f.json(w, map[string]any{})
					return true
				case kind == "inventory500" && strings.HasSuffix(path, "/branches"):
					f.apiError(w, 500, "injected")
					return true
				case kind == "nil inventory" && strings.HasSuffix(path, "/branches"):
					f.json(w, nil)
					return true
				}
				return reads(w, r)
			}
			out, err := runAuthority(t, f, opts)
			if kind == "rules500" && (!strings.Contains(out, "source=classic") || strings.Contains(out, "fence_unprotected")) {
				t.Fatalf("ruleset failure hid independent classic blocker or invented missing coverage: %s", out)
			}
			if err == nil || !strings.Contains(out, "BLOCKER") || f.setupWrites != 2 {
				t.Fatalf("certified incomplete preflight: %s %v", out, err)
			}
		})
	}
}

func TestAuthoritySetupPermissionGuidanceAndUnchangedRuntimeTrace(t *testing.T) {
	var baselineProblems, baselineWarnings []string
	var baselineCalls map[string]int
	for _, grant := range []string{"", "read"} {
		f := newAPI(t)
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasSuffix(r.URL.Path, "/access_tokens") {
				return false
			}
			token := f.object("installation_token")
			permissions := token["permissions"].(map[string]any)
			delete(permissions, "administration")
			if grant != "" {
				permissions["administration"] = grant
			}
			f.json(w, token)
			return true
		}
		problems, warnings, err := f.driver.ValidateInstallationPermissions(t.Context())
		must(t, err)
		calls := map[string]int{}
		for key, count := range f.calls {
			calls[key] = count
		}
		if grant == "" {
			baselineProblems, baselineWarnings = problems, warnings
			baselineCalls = map[string]int{}
			for key, count := range calls {
				baselineCalls[key] = count
			}
		} else if !reflect.DeepEqual(problems, baselineProblems) || !reflect.DeepEqual(warnings, baselineWarnings) || !reflect.DeepEqual(calls, baselineCalls) {
			t.Fatal("administration changed runtime permission traces", problems, warnings, calls)
		}
		var legacy, authorityOut bytes.Buffer
		must(t, f.driver.DiagnoseInstallationPermissions(t.Context(), &legacy))
		if strings.Contains(legacy.String(), "administration") || strings.Contains(legacy.String(), "Administration") {
			t.Fatal("legacy setup guidance changed")
		}
		must(t, f.driver.DiagnoseAuthorityPermissions(t.Context(), &authorityOut))
		for _, expected := range []string{"classic branch-protection settings", "neither grants ruleset writes nor guarantees bypass-list visibility"} {
			if !strings.Contains(authorityOut.String(), expected) {
				t.Fatal(authorityOut.String())
			}
		}
		if strings.Contains(authorityOut.String(), "RECOMMEND:") != (grant == "") {
			t.Fatal("wrong adoption diagnostic", authorityOut.String())
		}
		afterProblems, afterWarnings, err := f.driver.ValidateInstallationPermissions(t.Context())
		must(t, err)
		if !reflect.DeepEqual(afterProblems, problems) || !reflect.DeepEqual(afterWarnings, warnings) {
			t.Fatal("setup changed runtime")
		}
		// Authority setup alone fetches an unrestricted token grant for its
		// optional recommendation; runtime never adopts a new required grant.
		calls["POST app/installations/7/access_tokens"]++
		if !reflect.DeepEqual(f.calls, calls) {
			raw, _ := json.Marshal(f.calls)
			t.Fatal("permission diagnostic made extra requests", string(raw))
		}
	}
}

func TestRollbackProtectedFamilyExclusionsStayIndependent(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		t.Run(fmt.Sprint(escaped), func(t *testing.T) {
			f := rulesAPI(t)
			opts := rollbackOptions(t, f)
			policy := map[string]any{"id": 12, "name": "retained content", "source_type": "Organization", "source": "acme", "target": "branch", "enforcement": "active", "bypass_actors": []any{}, "rules": []map[string]any{{"type": "required_linear_history"}}}
			raw := `{"ref_name":{"include":["refs/heads/openmerge/**/*"],"exclude":["refs/heads/openmerge/queue/**/*","refs/heads/openmerge/fence/**/*"]}}`
			if escaped {
				raw = strings.ReplaceAll(raw, "/", `\/`)
			}
			policy["conditions"] = json.RawMessage(raw)
			f.setupRules[12] = policy
			f.override = rollbackReads(t, f, nil, nil)
			out, err := runAuthority(t, f, opts)
			if err == nil || !strings.Contains(out, `ref="refs/heads/openmerge/green/*"`) || !strings.Contains(out, `ref="refs/heads/openmerge/verify/*"`) {
				t.Fatalf("another family's exclusions hid protected refs: %s %v", out, err)
			}
			if strings.Contains(out, `ref="refs/heads/openmerge/queue/*"`) {
				t.Fatal("excluded queue produced a false blocker", out)
			}
		})
	}
}

func completeClassicFixture() map[string]any {
	return map[string]any{
		"required_status_checks":        nil,
		"required_pull_request_reviews": nil,
		"restrictions":                  nil,
		"required_linear_history":       map[string]any{"enabled": false},
		"allow_force_pushes":            map[string]any{"enabled": false},
		"lock_branch":                   map[string]any{"enabled": false},
	}
}

func TestRollbackWildcardFamilyWithoutExistingGreenRef(t *testing.T) {
	f := rulesAPI(t)
	opts := rollbackOptions(t, f)
	f.setupRules[12] = map[string]any{"id": 12, "name": "wildcard content", "source_type": "Organization", "source": "acme", "target": "branch", "enforcement": "active", "bypass_actors": []any{}, "rules": []map[string]any{{"type": "required_signatures"}}, "conditions": map[string]any{"ref_name": map[string]any{"include": []string{"refs/heads/openmerge/gr*/*"}, "exclude": []string{}}}}
	f.override = rollbackReads(t, f, nil, nil)
	out, err := runAuthority(t, f, opts)
	if err == nil || !strings.Contains(out, `ref="refs/heads/openmerge/green/*"`) || f.setupWrites != 2 {
		t.Fatalf("wildcard future green certified: %s %v", out, err)
	}
}

func TestRollbackClassicPartialReproduction(t *testing.T) {
	f := rulesAPI(t)
	opts := rollbackOptions(t, f)
	f.override = rollbackReads(t, f, map[string]any{"url": "https://api.github.com/protection"}, map[string]any{"enabled": false})
	out, err := runAuthority(t, f, opts)
	if err == nil || !strings.Contains(out, "incomplete classic protection response") || strings.Contains(out, "Rollback preflight passed") || f.setupWrites != 2 {
		t.Fatalf("partial classic certified: %s %v", out, err)
	}
}

func TestRollbackFamilyWildcardMatrix(t *testing.T) {
	for _, family := range []string{"fence", "queue", "green", "verify"} {
		prefix := "refs/heads/openmerge/" + family + "/"
		for _, tt := range []struct {
			include string
			exclude []string
			blocked bool
		}{
			{"refs/heads/openmerge/*/*", nil, true},
			{"refs/heads/openmerge/**/*", nil, true},
			{"refs/heads/openmerge/" + family[:1] + "*/*", nil, true},
			{"refs/heads/openmerge/" + family[:1] + "?" + family[2:] + "/*", nil, true},
			{"refs/heads/openmerge/" + family[:1] + "*" + family[len(family)-1:] + "/*/*", nil, true},
			{"~ALL", nil, true},
			{"~ALL", []string{prefix + "*"}, true}, // Nested descendants remain covered.
			{"~ALL", []string{prefix + "**/*"}, false},
			{prefix + "*", []string{prefix + "*"}, false},
			{prefix + "*/*", []string{prefix + "*"}, true},
			{"refs/heads/openmerge/other/**/*", nil, false},
		} {
			t.Run(family+"/"+tt.include+"/"+strings.Join(tt.exclude, ","), func(t *testing.T) {
				conditions := refConditions{}
				conditions.RefName.Include, conditions.RefName.Exclude = []string{tt.include}, tt.exclude
				raw, err := json.Marshal(conditions)
				must(t, err)
				rule := Ruleset{ID: 12, Name: "wildcard content", Target: "branch", Enforcement: "active", Conditions: raw, BypassActors: []BypassActor{}, Rules: []RepositoryRule{{Type: "required_signatures"}}}
				blocked := false
				for _, p := range rollbackRules(true, []Ruleset{rule}, "main", "main", 42) {
					blocked = blocked || p.Code == RefBlocked && p.Ref == prefix+"*"
				}
				if blocked != tt.blocked {
					t.Fatalf("blocked = %v, want %v", blocked, tt.blocked)
				}
			})
		}
	}
}
