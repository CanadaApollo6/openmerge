package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestImportPullRequestParameterAllowlist(t *testing.T) {
	for _, key := range []string{"allowed_merge_methods", "required_reviewers", "future_parameter", "dismiss_stale_reviews"} {
		t.Run(key, func(t *testing.T) {
			r := ProtectionReport{}
			raw := json.RawMessage(fmt.Sprintf(`{"%s":[{"pattern":"src/**","team_id":7,"count":2}]}`, key))
			if err := r.translateRule("main", "ruleset:1", RepositoryRule{Type: "pull_request", Parameters: raw}, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			item := findImportItem(t, r, "main", "ruleset:1", "pull-request-parameter-"+key)
			if item.Classification != "unsupported" || !strings.Contains(item.Message, key) {
				t.Fatalf("dropped parameter: %+v", item)
			}
		})
	}
	for _, value := range []string{`{"enabled":true,"allowed_actors":[{"actor_id":7,"actor_type":"Team"}]}`, `{"enabled":false,"future":true}`, `null`, `{}`} {
		r := ProtectionReport{}
		if err := r.translateRule("main", "ruleset:1", RepositoryRule{Type: "pull_request", Parameters: json.RawMessage(`{"dismissal_restriction":` + value + `}`)}, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		item := findImportItem(t, r, "main", "ruleset:1", "dismissal-restrictions")
		if item.Classification != "unsupported" || !strings.Contains(item.Message, value) {
			t.Fatalf("dismissal lost: %+v", item)
		}
	}
}

func TestImportMembershipIntersection(t *testing.T) {
	for _, classic := range []bool{false, true} {
		for _, readable := range []bool{false, true} {
			t.Run(fmt.Sprintf("classic=%t/readable=%t", classic, readable), func(t *testing.T) {
				f := newProtectionFixture(t)
				f.rules[0].Rules = []RepositoryRule{{Type: "update"}}
				f.rules[0].BypassActors = []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}
				f.rules[1].BypassActors = []BypassActor{{ActorType: "RepositoryRole", ActorID: new(int64(4)), BypassMode: "always"}}
				f.classic["restrictions"] = nil
				if classic {
					f.rules = f.rules[:1]
					f.classic["restrictions"] = &classicActors{Users: []classicIdentity{{ID: 1, Login: "alice"}}}
				}
				original := f.server(t)
				defer original.Close()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if strings.HasSuffix(req.URL.Path, "/collaborators") {
						_, _ = fmt.Fprint(w, `[{"id":1,"login":"alice","role_name":"maintain","permissions":{"push":true,"maintain":true}}]`)
						return
					}
					if strings.HasPrefix(req.URL.Path, "/teams/") && !readable {
						w.WriteHeader(403)
						_, _ = fmt.Fprint(w, `{"message":"Forbidden"}`)
						return
					}
					original.Config.Handler.ServeHTTP(w, req)
				}))
				defer server.Close()
				r, err := importProtection(t.Context(), ImportOptions{Repo: "acme/project"}, "admin-test-token", server.URL+"/")
				if err != nil {
					t.Fatal(err)
				}
				key := "principal-User:1"
				if !readable {
					key = "unresolved-memberships"
				}
				item := findImportItem(t, r, "main", "authority", key)
				if !item.Blocking {
					t.Fatalf("authority lost: %+v", item)
				}
				if err := r.accept([]string{item.ID}); err == nil {
					t.Fatal("accepted unresolved authority")
				}
			})
		}
	}
}

func TestImportUnknownCheckCoverageNeedsAcceptance(t *testing.T) {
	for _, workflows := range [][]importWorkflow{nil, {{Name: "ci", Queue: "unknown (path filters)"}}, {{Name: "ci", Queue: "unknown (reusable workflow caller chain unresolved)"}}} {
		r := ProtectionReport{}
		r.requiredChecks("main", "test", importStatusChecks{Checks: []importRequiredCheck{{Context: "ci"}}}, checkEvidence{"ci": {"check:1": true}}, workflows)
		item := findImportItem(t, r, "main", "test", "check-ci")
		if item.Classification != "unsupported" || strings.Contains(item.Fragment, "merge_when:\n") {
			t.Fatalf("false clean coverage: %+v", item)
		}
		if err := r.accept(nil); err != nil || r.Ready {
			t.Fatalf("unknown coverage ready: %v", err)
		}
	}
}

func TestImportClassicTogglesAndLockedLanding(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		r := ProtectionReport{}
		p := &importClassicProtection{Signatures: &importToggle{}, ForcePushes: importToggle{enabled}, Deletions: importToggle{enabled}, Creations: importToggle{enabled}, Admins: importToggle{enabled}, Lock: importToggle{enabled}}
		if err := r.translateClassic("main", p, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"allow_force_pushes", "allow_deletions", "block_creations", "enforce_admins", "lock_branch"} {
			item := findImportItem(t, r, "main", "classic", key)
			if !strings.Contains(item.Message, fmt.Sprintf("enabled=%t", enabled)) || key == "lock_branch" && item.Blocking != enabled {
				t.Fatalf("toggle lost: %+v", item)
			}
		}
	}
}

func TestImportIDsCannotCollideOrReuseChangedParameters(t *testing.T) {
	r := ProtectionReport{}
	r.requiredChecks("main", "test", importStatusChecks{Contexts: []string{"ci", "results-ci", "result-semantics-ci"}}, nil, nil)
	ids := map[string]bool{}
	for _, item := range r.Items {
		if ids[item.ID] {
			t.Fatalf("duplicate ID %s", item.ID)
		}
		ids[item.ID] = true
	}
	reviewID := func(count int) string {
		r := ProtectionReport{}
		r.reviews("main", "test", importReviews{Count: count, Stale: true}, []string{"alice"})
		return findImportItem(t, r, "main", "test", "reviews").ID
	}
	if reviewID(1) == reviewID(2) {
		t.Fatal("strengthened review count reuses acceptance")
	}
	checkID := func(app int64) string {
		r := ProtectionReport{}
		r.requiredChecks("main", "test", importStatusChecks{Checks: []importRequiredCheck{{Context: "ci", AppID: &app}}}, nil, nil)
		return findImportItem(t, r, "main", "test", "check-ci").ID
	}
	if checkID(1) == checkID(2) {
		t.Fatal("changed App requirement reuses acceptance")
	}
	duplicateID := func(app int64) string {
		r := ProtectionReport{}
		r.requiredChecks("main", "test", importStatusChecks{Checks: []importRequiredCheck{{Context: "ci", AppID: new(int64(1))}, {Context: "ci", AppID: &app}}}, nil, nil)
		return findImportItem(t, r, "main", "test", "check-ci").ID
	}
	if duplicateID(2) == duplicateID(3) {
		t.Fatal("changed duplicate App requirement reuses acceptance")
	}

}

func TestImportReconcilesApprovalDraftExactly(t *testing.T) {
	for _, counts := range [][]int{{1, 3, 2}, {3, 1, 2}} {
		r := ProtectionReport{}
		for i, count := range counts {
			r.reviews("main", fmt.Sprint(i), importReviews{Count: count, Owners: i == 1}, []string{"alice", "bob"})
		}
		if err := r.accept(nil); err != nil {
			t.Fatal(err)
		}
		want := `approvals:
    default: any_writer
    rules:
        - require:
            count: 3
            group: imported_reviewers
          when:
            touches:
                - '**'
changes:
    pull_requests: required
groups:
    imported_reviewers:
        - alice
        - bob
`
		if !strings.Contains(r.ProposedConfig, want) || strings.Count(r.ProposedConfig, "approvals:\n") != 1 || !strings.Contains(r.ProposedConfig, "one eligible owner per changed owned file") || strings.Contains(r.ProposedConfig, "when: {}") {
			t.Fatalf("wrong reconciled draft:\n%s", r.ProposedConfig)
		}
	}
}

func TestImportOwnerCoverageStaysToVerify(t *testing.T) {
	for _, data := range []string{"* @@not-a-real-owner!!", "* @acme/maintainers"} {
		r := ProtectionReport{}
		r.coverage(parseImportOwners("CODEOWNERS", data))
		for _, item := range r.Items {
			if !item.Blocking || !strings.Contains(item.Message, "To verify at switch") {
				t.Fatalf("unverified owner declared resolved: %+v", item)
			}
		}
	}
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"*/", "CODEOWNERS", false}, {"*/", ".openmerge.yml", false}, {"*/", ".openmerge.yaml", false},
		{"*/", "dir/CODEOWNERS", true}, {"/docs/", "docs", false}, {"/docs/", "docs/file", true}, {"docs/", "a/docs/file", true},
	} {
		if got := ownerPatternMatches(tc.pattern, tc.path); got != tc.want {
			t.Errorf("%s %s=%t", tc.pattern, tc.path, got)
		}
	}
}

func TestImportGoldenReport(t *testing.T) {
	f := newProtectionFixture(t)
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := r.Write(&wire, true); err != nil {
		t.Fatal(err)
	}
	path := "testdata/import-protection/report.golden.json"
	if os.Getenv("UPDATE_IMPORT_GOLDEN") == "1" {
		if err := os.WriteFile(path, wire.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire.Bytes(), want) {
		t.Fatal("report differs from golden JSON")
	}
	var human bytes.Buffer
	if err := r.Write(&human, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(human.String(), "Ready: false; installation/authority blockers:") {
		t.Fatal("readiness summary is not first")
	}
	for _, rs := range f.rules {
		for _, a := range rs.BypassActors {
			count := 0
			for _, item := range r.Items {
				if item.Source == fmt.Sprintf("ruleset:%d", rs.ID) && strings.HasPrefix(item.ID, "bypass-"+actorKey(a)+"-") {
					count++
					if item.Base != "" {
						t.Fatal("inventory repeated by base")
					}
				}
			}
			if count != 1 {
				t.Fatalf("inventory count=%d", count)
			}
		}
	}
	// Reordering the source rules must not alter the reconciled queue requirements.
	slices.Reverse(f.rules)
	other, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range r.Bases {
		if r.queueDrafts[base].Count != other.queueDrafts[base].Count {
			t.Fatal("order changed approval count")
		}
	}
}
