package github

import (
	"net/http"
	"strings"
	"testing"
)

func TestLinearHistoryUsesEitherReadableProtectionSource(t *testing.T) {
	for _, tc := range []struct {
		name, classic, rules string
		required, unknown    bool
	}{
		{"classic", "required", "unreadable", true, false},
		{"ruleset", "unreadable", "required", true, false},
		{"classic allows but ruleset requires", "allows", "required", true, false},
		{"no classic", "absent", "required", true, false},
		{"not required", "allows", "allows", false, false},
		{"neither protects", "absent", "allows", false, false},
		{"unknown classic", "unreadable", "allows", false, true},
		{"unknown rules", "allows", "unreadable", false, true},
		{"both unknown", "unreadable", "unreadable", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			rulePages := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				classic := strings.Contains(r.URL.Path, "/branches/") && !strings.Contains(r.URL.Path, "/rules/")
				rules := strings.Contains(r.URL.Path, "/rules/branches/")
				if !classic && !rules {
					return false
				}
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer secret-installation-1" || !strings.Contains(r.URL.EscapedPath(), "release%2Fv1") {
					t.Errorf("wrong policy request: %s %s", r.Method, r.URL.EscapedPath())
				}
				mode := tc.classic
				if rules {
					mode = tc.rules
					rulePages++
				}
				switch {
				case mode == "unreadable":
					f.apiError(w, 403, "secret policy details")
				case classic && mode == "absent":
					f.apiError(w, 404, "Branch not protected")
				case classic:
					f.json(w, map[string]any{"required_linear_history": map[string]bool{"enabled": mode == "required"}})
				case mode == "required" && r.URL.Query().Get("page") == "1":
					w.Header().Set("Link", "<"+f.server.URL+r.URL.Path+"?page=2>; rel=\"next\"")
					f.json(w, []any{map[string]string{"type": "non_fast_forward"}})
				case mode == "required":
					f.json(w, []any{map[string]string{"type": "required_linear_history"}})
				default:
					f.json(w, []any{})
				}
				return true
			}
			required, err := f.driver.LinearHistoryRequired(t.Context(), testRepo, "refs/heads/release/v1")
			if required != tc.required || (err != nil) != tc.unknown || err != nil && strings.Contains(err.Error(), "secret policy details") {
				t.Fatalf("required=%t err=%v", required, err)
			}
			if tc.rules == "required" && rulePages != 2 {
				t.Fatalf("rules pagination: %d pages", rulePages)
			}
		})
	}
}
