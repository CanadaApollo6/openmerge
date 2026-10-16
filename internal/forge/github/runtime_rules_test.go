package github

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestRuntimeRulesUseInstallationReadsIncludingParentsAndPages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func([]Ruleset)
		problem  string
		warnings int
	}{
		{"valid", func([]Ruleset) {}, "", 0},
		{"hidden bypass", func(r []Ruleset) { r[0].BypassActors = nil; r[1].BypassActors = nil }, "", 2},
		{"visible missing bypass", func(r []Ruleset) { r[0].BypassActors = []BypassActor{} }, "base_bypass_missing", 0},
		{"hidden structurally broken fence", func(r []Ruleset) { r[1].BypassActors = nil; r[1].Rules = r[1].Rules[:2] }, "fence_unprotected", 1},
		{"hidden malformed conditions", func(r []Ruleset) { r[0].BypassActors = nil; r[0].Conditions = []byte(`{}`) }, "rules_unknown", 1},
		{"missing rule details", func(r []Ruleset) { r[0].Rules = nil }, "rules_unknown", 0},
		{"visible extra fence bypass", func(r []Ruleset) { r[1].BypassActors = append(r[1].BypassActors, r[0].BypassActors[1]) }, "fence_unprotected", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			rules := fixtureRules(t)
			tc.edit(rules)
			details := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
				if path == "repos/acme/project" {
					f.json(w, map[string]any{"default_branch": "main"})
					return true
				}
				if !strings.HasPrefix(path, "repos/acme/project/rulesets") {
					return false
				}
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer secret-installation-1" || r.URL.Query().Get("includes_parents") != "true" {
					t.Errorf("runtime used wrong method/credential/parents: %s %s", r.Method, r.URL)
				}
				if path == "repos/acme/project/rulesets" {
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					if page == 1 {
						w.Header().Set("Link", "<"+f.server.URL+"/api/v3/repos/acme/project/rulesets?includes_parents=true&per_page=100&page=2>; rel=\"next\"")
					}
					f.json(w, []map[string]any{{"id": rules[page-1].ID, "name": rules[page-1].Name}})
					return true
				}
				id, _ := strconv.ParseInt(strings.TrimPrefix(path, "repos/acme/project/rulesets/"), 10, 64)
				for _, rule := range rules {
					if rule.ID == id {
						details++
						f.json(w, rule)
						return true
					}
				}
				t.Fatalf("unexpected detail path %s", path)
				return true
			}
			problems, warnings, err := f.driver.ValidateRepositoryRules(context.Background(), forge.RepoRef{Path: "acme/project"}, "main")
			must(t, err)
			if details != 2 || len(warnings) != tc.warnings || tc.problem == "" && len(problems) > 0 || tc.problem != "" && !strings.Contains(strings.Join(problems, ";"), tc.problem) {
				t.Fatalf("details=%d problems=%v warnings=%v", details, problems, warnings)
			}
		})
	}
}

func TestRuntimeRulesErrorsAndActualDefaultBranch(t *testing.T) {
	for _, mode := range []string{"read denied", "missing default", "repeated ID", "wrong detail ID", "release base"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			rules := fixtureRules(t)
			rules[0].BypassActors = []BypassActor{}
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/acme/project")
				if path == "" {
					switch mode {
					case "read denied":
						f.apiError(w, 403, "secret response never shown")
					case "missing default":
						f.json(w, map[string]any{})
					default:
						f.json(w, map[string]any{"default_branch": "main"})
					}
					return true
				}
				if path == "/rulesets" {
					if mode == "repeated ID" {
						f.json(w, []map[string]int{{"id": 10}, {"id": 10}})
					} else {
						f.json(w, []map[string]int{{"id": 10}, {"id": 11}})
					}
					return true
				}
				if strings.HasPrefix(path, "/rulesets/") {
					i := 0
					if strings.HasSuffix(path, "11") {
						i = 1
					}
					if mode == "wrong detail ID" {
						rules[i].ID = 99
					}
					f.json(w, rules[i])
					return true
				}
				return false
			}
			problems, _, err := f.driver.ValidateRepositoryRules(t.Context(), forge.RepoRef{Path: "acme/project"}, "release/v1")
			if mode == "release base" {
				must(t, err)
				if len(problems) > 0 {
					t.Fatalf("~DEFAULT_BRANCH matched release base: %v", problems)
				}
			} else if err == nil || strings.Contains(err.Error(), "secret response") {
				t.Fatalf("missing or unsafe read error: %v", err)
			}
		})
	}
}
