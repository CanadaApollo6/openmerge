package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"go.yaml.in/yaml/v3"
)

type protectionFixture struct {
	rules       []Ruleset
	classic     map[string]any
	files       map[string]map[string]string
	failPath    string
	failStatus  int
	failMessage string
	noAdmin     bool
	requests    []string
}

func readImportFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/import-protection/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func newProtectionFixture(t *testing.T) *protectionFixture {
	t.Helper()
	f := &protectionFixture{classic: make(map[string]any), files: map[string]map[string]string{
		"main-tip": {
			".openmerge.yml":              "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n- name: releases\n  base: release/*\n  merge_when: [check-success = ci]\n",
			".github/CODEOWNERS":          "* @acme/maintainers\n/docs/ @alice\n",
			"CODEOWNERS":                  "* @ignored-lower-precedence\n",
			".github/workflows/queue.yml": string(readImportFixture(t, "queue.workflow")),
			".github/workflows/pr.yml":    string(readImportFixture(t, "pr.workflow")),
		},
		"release-tip": {"CODEOWNERS": "* @acme/release-team\n/docs/ @bob\n"},
	}}
	if err := json.Unmarshal(readImportFixture(t, "rulesets.json"), &f.rules); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readImportFixture(t, "classic.json"), &f.classic); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *protectionFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.Method != "GET" {
			t.Errorf("unexpected mutation %s", r.Method)
			w.WriteHeader(500)
			return
		}
		if r.Header.Get("Authorization") != "Bearer admin-test-token" {
			t.Errorf("wrong auth: %q", r.Header.Get("Authorization"))
		}
		path := strings.TrimPrefix(r.URL.Path, "/repos/acme/project")
		send := func(v any) {
			t.Helper()
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		if path == f.failPath && f.failStatus != 0 {
			w.WriteHeader(f.failStatus)
			send(map[string]string{"message": f.failMessage})
			return
		}
		switch {
		case path == "":
			send(map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": !f.noAdmin}})
		case path == "/rulesets":
			send(f.rules)
		case strings.HasPrefix(path, "/rulesets/"):
			for _, rs := range f.rules {
				if path == fmt.Sprintf("/rulesets/%d", rs.ID) {
					send(rs)
					return
				}
			}
			t.Errorf("unknown ruleset %s", path)
		case path == "/branches":
			send([]any{map[string]any{"name": "main", "commit": map[string]string{"sha": "main-tip"}}, map[string]any{"name": "release/1", "commit": map[string]string{"sha": "release-tip"}}, map[string]any{"name": "openmerge/queue/ignored", "commit": map[string]string{"sha": "queue-tip"}}})
		case strings.HasPrefix(path, "/git/trees/"):
			sha := strings.TrimPrefix(path, "/git/trees/")
			var entries []map[string]string
			for file := range f.files[sha] {
				entries = append(entries, map[string]string{"path": file, "type": "blob", "mode": "100644", "sha": base64.RawURLEncoding.EncodeToString([]byte(sha + ":" + file))})
			}
			send(map[string]any{"truncated": false, "tree": entries})
		case strings.HasPrefix(path, "/git/blobs/"):
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(path, "/git/blobs/"))
			if err != nil {
				t.Error(err)
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			send(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(f.files[parts[0]][parts[1]]))})
		case r.URL.Path == "/teams/7/members":
			send([]classicIdentity{{ID: 1, Login: "alice"}})
		case path == "/collaborators":
			send([]any{map[string]any{"login": "alice", "permissions": map[string]bool{"push": true}}, map[string]any{"login": "bob", "permissions": map[string]bool{"admin": true}}, map[string]any{"login": "reader", "permissions": map[string]bool{"pull": true}}})
		case strings.HasSuffix(path, "/protection"):
			if strings.Contains(path, "release/1") {
				w.WriteHeader(404)
				send(map[string]string{"message": "Branch not protected"})
			} else {
				send(f.classic)
			}
		case strings.HasSuffix(path, "/check-runs"):
			send(map[string]any{"check_runs": []any{map[string]any{"name": "ci", "app": map[string]int{"id": 15368}}, map[string]any{"name": "pr-only", "app": map[string]int{"id": 15368}}, map[string]any{"name": "shared", "app": map[string]int{"id": 15368}}}})
		case strings.HasSuffix(path, "/statuses"):
			send([]any{map[string]any{"context": "shared", "creator": map[string]int{"id": 2}}})
		default:
			t.Errorf("unexpected API request %s", r.URL.RequestURI())
			w.WriteHeader(500)
			send(map[string]string{"message": "unexpected route"})
		}
	}))
}
func fixtureReport(t *testing.T, f *protectionFixture, opts ImportOptions) (ProtectionReport, error) {
	t.Helper()
	server := f.server(t)
	defer server.Close()
	opts.Repo = "acme/project"
	return importProtection(context.Background(), opts, "admin-test-token", server.URL+"/")
}
func findImportItem(t *testing.T, r ProtectionReport, base, source, key string) ProtectionItem {
	t.Helper()
	for _, item := range r.Items {
		if item.Base == base && item.Source == source && strings.HasPrefix(item.ID, key+"-") {
			return item
		}
	}
	t.Fatalf("missing item %s %s %s", base, source, key)
	return ProtectionItem{}
}
func TestImportProtectionReport(t *testing.T) {
	f := newProtectionFixture(t)
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Bases, []string{"main", "release/1"}) || r.GoverningCommit != "main-tip" || r.Ready {
		t.Fatalf("wrong report metadata: %+v", r)
	}
	for _, tc := range []struct {
		base, source, key, class, contains string
		blocking                           bool
	}{
		{"main", "ruleset:11", "reviews", "weaker", "merge-base", false},
		{"main", "classic", "reviews", "stricter", "Head-bound", false},
		{"main", "ruleset:11", "owner-review", "weaker", "governing", false},
		{"main", "ruleset:11", "last-push", "unsupported", "later pusher", false},
		{"main", "ruleset:11", "check-ci", "weaker", "App-pinned", false},
		{"main", "ruleset:11", "check-pr-only", "unsupported", "only on pull requests", false},
		{"main", "ruleset:11", "check-shared", "unsupported", "ambiguous", false},
		{"main", "ruleset:11", "check-missing", "unsupported", "missing", false},
		{"main", "ruleset:11", "result-semantics:ci", "stricter", "neutral and skipped", false},
		{"main", "ruleset:11", "up-to-date", "equivalent", "post_merge", false},
		{"main", "ruleset:11", "conversation-resolution", "unsupported", "no OpenMerge equivalent", false},
		{"main", "ruleset:11", "deployments", "unsupported", "no OpenMerge equivalent", false},
		{"main", "classic", "signatures", "unsupported", "unsigned", false},
		{"main", "classic", "linear-history", "unsupported", "merge-method", false},
		{"release/1", "CODEOWNERS", "owners-comparison", "unsupported", "@acme/release-team", false},
		{"main", "authority", "writer-Integration:42", "equivalent", "Bots cannot", true},
		{"main", "authority", "writer-Team:7", "equivalent", "every applicable", true},
		{"", "installation", "served-default", "equivalent", "must be served", false},
		{"", "installation", "bypass-verification", "equivalent", "To verify at switch", true},
	} {
		item := findImportItem(t, r, tc.base, tc.source, tc.key)
		if item.Classification != tc.class || item.Blocking != tc.blocking || !strings.Contains(item.Message, tc.contains) {
			t.Errorf("%s: %+v", tc.key, item)
		}
	}
	for _, needle := range []string{"alice", "bob", "approvals:", "groups:", "Stage A", "changes:", "pull_requests: required"} {
		if !strings.Contains(r.ProposedConfig, needle) {
			t.Errorf("draft missing %s", needle)
		}
	}
	if strings.Contains(r.ProposedConfig, "reader") {
		t.Fatal("read-only collaborator was eligible")
	}
	if !strings.Contains(findImportItem(t, r, "main", "ruleset:11", "check-ci").Message, "queue pushes=yes") {
		t.Fatal("queue workflow not found")
	}
	if !strings.Contains(findImportItem(t, r, "release/1", "CODEOWNERS", "owners-comparison").Message, "@bob") {
		t.Fatal("not every CODEOWNERS difference listed")
	}
	for _, item := range r.Items {
		if item.Source == "ruleset:12" && strings.Contains(item.ID, "bypass-Integration:99") {
			if !strings.Contains(item.Message, "Organization acme") {
				t.Fatal("inherited actor lost source")
			}
		}
	}
	var human, wire bytes.Buffer
	if err := r.Write(&human, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Write(&wire, true); err != nil {
		t.Fatal(err)
	}
	var decoded ProtectionReport
	if err := json.Unmarshal(wire.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProposedConfig != r.ProposedConfig || decoded.ContributingGuide != importGuide || !strings.Contains(human.String(), "@openmerge queue") {
		t.Fatal("output lost information")
	}
}
func TestImportPermissionFailuresAreNotPartialReports(t *testing.T) {
	for _, tc := range []struct{ path, permission string }{
		{"", "repository admin"}, {"/rulesets", "Administration read"}, {"/rulesets/12", "inherited"}, {"/branches/main/protection", "Administration read"}, {"/git/trees/main-tip", "Contents read"}, {"/commits/main-tip/check-runs", "Checks read"}, {"/commits/main-tip/statuses", "Commit statuses read"},
	} {
		for _, status := range []int{403, 404} {
			t.Run(fmt.Sprintf("%s/%d", tc.path, status), func(t *testing.T) {
				f := newProtectionFixture(t)
				f.failPath, f.failStatus, f.failMessage = tc.path, status, "Not Found"
				r, err := fixtureReport(t, f, ImportOptions{})
				if err == nil || !strings.Contains(err.Error(), tc.permission) || !errors.Is(err, forge.ErrRulesUnavailable) || len(r.Items) != 0 {
					t.Fatalf("partial or unclear report: %+v, %v", r, err)
				}
			})
		}
	}
}
func TestImportExplicitUnprotectedAndAdminVisibility(t *testing.T) {
	for _, tc := range []string{"unprotected", "no-admin", "hidden-bypass"} {
		t.Run(tc, func(t *testing.T) {
			f := newProtectionFixture(t)
			switch tc {
			case "unprotected":
				f.failPath, f.failStatus, f.failMessage = "/branches/main/protection", 404, "Branch not protected"
			case "no-admin":
				f.noAdmin = true
			case "hidden-bypass":
				f.rules[1].BypassActors = nil
			}
			_, err := fixtureReport(t, f, ImportOptions{})
			if (tc == "unprotected") != (err == nil) {
				t.Fatalf("%s: %v", tc, err)
			}
		})
	}
}
func TestImportAcceptanceMatrix(t *testing.T) {
	for _, class := range []string{"equivalent", "stricter", "weaker", "unsupported"} {
		for _, blocking := range []bool{false, true} {
			for _, accepted := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/block=%t/accept=%t", class, blocking, accepted), func(t *testing.T) {
					r := ProtectionReport{}
					r.add("main", "test", "requirement", class, blocking, "gap", "approvals:\n  default: any_writer\n")
					var ids []string
					if accepted {
						ids = []string{r.Items[0].ID}
					}
					err := r.accept(ids)
					canAccept := !blocking && (class == "weaker" || class == "unsupported")
					if accepted && !canAccept {
						if err == nil {
							t.Fatal("accepted a non-relaxation/prerequisite")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					wantReady := !blocking && (class == "equivalent" || class == "stricter" || accepted)
					if r.Ready != wantReady {
						t.Fatalf("ready=%t, want %t", r.Ready, wantReady)
					}
					if accepted && !strings.Contains(r.ProposedConfig, "# ACCEPTED "+ids[0]) {
						t.Fatal("acceptance not recorded")
					}
				})
			}
		}
	}
	r := ProtectionReport{}
	r.add("main", "a", "first", "weaker", false, "first gap", "# first")
	r.add("main", "b", "second", "unsupported", false, "second gap", "# second")
	if err := r.accept([]string{r.Items[0].ID}); err != nil || r.Ready {
		t.Fatal("partial acceptance passed")
	}
	if err := r.accept([]string{r.Items[0].ID, r.Items[1].ID}); err != nil || !r.Ready {
		t.Fatal("complete acceptance did not pass")
	}
	if err := r.accept([]string{"typo"}); err == nil {
		t.Fatal("unknown ID accepted")
	}
}
func TestImportReadOnlyTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; _, _ = io.WriteString(w, "{}") }))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "admin-test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = getOnlyTransport{base: c.http.Transport}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		var result any
		if err := c.request(context.Background(), method, "repos/acme/project", nil, &result); err == nil {
			t.Fatalf("%s permitted", method)
		}
	}
	if calls != 0 {
		t.Fatalf("%d mutations reached HTTP server", calls)
	}
	var result any
	if err := c.request(context.Background(), "GET", "repos/acme/project", nil, &result); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("GET did not reach server")
	}
}
func TestWorkflowPushInspection(t *testing.T) {
	for _, tc := range []struct {
		data, want string
		onlyPR     bool
	}{
		{"on: push", "yes", false}, {"on: [pull_request]", "no", true}, {"on: [push, pull_request]", "yes", false},
		{"on:\n  push:\n    branches: ['openmerge/queue/**']", "yes", false},
		{"on:\n  push:\n    branches: [main]", "no", false},
		{"on:\n  push:\n    branches: ['openmerge/queue/**']\n    paths: ['src/**']", "unknown", false},
		{"on:\n  push:\n    branches: ['**', '!openmerge/**']", "unknown", false},
		{"on:\n  push:\n    branches-ignore: ['openmerge/**']", "no", false},
		{"on:\n  push:\n    tags: ['v*']", "no", false},
	} {
		var doc struct {
			On yaml.Node `yaml:"on"`
		}
		if err := yaml.Unmarshal([]byte(tc.data), &doc); err != nil {
			t.Fatal(err)
		}
		got, onlyPR := workflowQueue(doc.On)
		if !strings.HasPrefix(got, tc.want) || onlyPR != tc.onlyPR {
			t.Errorf("%s: %s prOnly=%t", tc.data, got, onlyPR)
		}
	}
}
func TestCodeownersCoverageAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		data, path string
		want       []string
	}{
		{"* @all\n/.openmerge.yml\n", ".openmerge.yml", []string{}},
		{"*.yml @yaml\n/.github/ @team\n", ".github/openmerge.yml", []string{"@team"}},
		{"docs/ @docs\n", "src/docs/file.go", []string{"@docs"}},
		{"/docs/ @docs\n", "src/docs/file.go", nil},
		{"CODEOWNERS @owners\n", ".github/CODEOWNERS", []string{"@owners"}},
		{"/docs/**/file.go @nested\n", "docs/a/b/file.go", []string{"@nested"}},
	} {
		owners := parseImportOwners("CODEOWNERS", tc.data)
		if got := owners.effective(tc.path); !slices.Equal(got, tc.want) {
			t.Errorf("%q for %s: %v want %v", tc.data, tc.path, got, tc.want)
		}
	}
	f := newProtectionFixture(t)
	f.files["main-tip"][".github/CODEOWNERS"] = "* @acme/maintainers\n/.openmerge.yml\n/.github/openmerge.yaml person@example.com\n"
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".openmerge.yml", ".github/openmerge.yaml"} {
		if !findImportItem(t, r, "", "governing-CODEOWNERS", "coverage-"+path).Blocking {
			t.Fatalf("unowned %s passed", path)
		}
	}
}
func TestBypassOnlyBlocksEffectiveAuthority(t *testing.T) {
	f := newProtectionFixture(t)
	f.rules[0].Rules = append(f.rules[0].Rules, RepositoryRule{Type: "update"})
	f.rules[0].BypassActors = []BypassActor{{ActorID: new(int64(42)), ActorType: "Integration", BypassMode: "always"}}
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range r.Items {
		if item.Source == "authority" && item.Blocking && strings.Contains(item.ID, "Team:7") {
			t.Fatalf("actor bypasses only one ruleset: %+v", item)
		}
	}
	if !findImportItem(t, r, "main", "authority", "writer-Integration:42").Blocking {
		t.Fatal("effective bot authority missing")
	}
}
func TestImportBaseDiscoveryCannotBeNarrowed(t *testing.T) {
	f := newProtectionFixture(t)
	r, err := fixtureReport(t, f, ImportOptions{Bases: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.Bases, "release/1") {
		t.Fatal("--base narrowed configured bases")
	}
	f = newProtectionFixture(t)
	f.files["main-tip"][".openmerge.yml"] = "version: 1\nqueues: [{name: releases, base: 'release/*', merge_when: ['check-success = ci']}]\n"
	r, err = fixtureReport(t, f, ImportOptions{Bases: []string{"main"}})
	if err != nil {
		t.Fatal(err)
	}
	if !findImportItem(t, r, "", "installation", "served-default").Blocking {
		t.Fatal("--base falsely served default branch")
	}
}

func TestImportCheckMappingRequiresUniqueName(t *testing.T) {
	for _, tc := range []struct {
		name      string
		checks    []importRequiredCheck
		evidence  checkEvidence
		workflows []importWorkflow
		class     string
	}{
		{"unique", []importRequiredCheck{{Context: "ci"}}, checkEvidence{"ci": {"check:1": true}}, []importWorkflow{{Name: "ci", Queue: "yes"}}, "equivalent"},
		{"duplicate-required-producers", []importRequiredCheck{{Context: "ci", AppID: new(int64(1))}, {Context: "ci", AppID: new(int64(2))}}, checkEvidence{"ci": {"check:1": true}}, nil, "unsupported"},
		{"shared-workflow-status", []importRequiredCheck{{Context: "ci"}}, checkEvidence{"ci": {"status:1": true}}, []importWorkflow{{Name: "ci", Queue: "yes"}}, "unsupported"},
		{"two-workflows", []importRequiredCheck{{Context: "ci"}}, checkEvidence{"ci": {"check:1": true}}, []importWorkflow{{Path: "a", Name: "ci", Queue: "yes"}, {Path: "b", Name: "ci", Queue: "yes"}}, "unsupported"},
		{"base-push-only", []importRequiredCheck{{Context: "ci"}}, checkEvidence{"ci": {"check:1": true}}, []importWorkflow{{Name: "ci", Queue: "no"}}, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ProtectionReport{}
			r.requiredChecks("main", "test", importStatusChecks{Checks: tc.checks}, tc.evidence, tc.workflows)
			item := findImportItem(t, r, "main", "test", "check-ci")
			if item.Classification != tc.class {
				t.Fatalf("%+v", item)
			}
			if tc.class == "unsupported" && strings.Contains(item.Fragment, "check-success =") {
				t.Fatal("ambiguous/unsupported producer mapped to a check condition")
			}
			ids := map[string]bool{}
			for _, entry := range r.Items {
				if ids[entry.ID] {
					t.Fatalf("duplicate item ID %s", entry.ID)
				}
				ids[entry.ID] = true
			}
		})
	}
}
func TestImportAcceptsActualStableReportIDs(t *testing.T) {
	f := newProtectionFixture(t)
	first, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var accept []string
	for _, item := range first.Items {
		if !item.Blocking && (item.Classification == "weaker" || item.Classification == "unsupported") {
			accept = append(accept, item.ID)
		}
	}
	slices.Reverse(f.rules)
	second, err := fixtureReport(t, f, ImportOptions{Accept: accept})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range second.Items {
		if slices.Contains(accept, item.ID) && !item.Accepted {
			t.Fatalf("ID changed or wasn't accepted: %s", item.ID)
		}
	}
	if second.Ready {
		t.Fatal("installation prerequisites accepted away")
	}
	if !strings.Contains(second.ProposedConfig, "# ACCEPTED "+accept[0]) {
		t.Fatal("acceptance comment missing")
	}
}

func TestPROnlyBypassIsStillAnEffectiveWriter(t *testing.T) {
	f := newProtectionFixture(t)
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := findImportItem(t, r, "release/1", "authority", "writer-Integration:99")
	if !item.Blocking || !strings.Contains(item.Message, "PR-only") {
		t.Fatalf("PR-only bypass permits a merge outside the queue: %+v", item)
	}
	for _, item := range r.Items {
		if item.Base == "main" && item.Source == "authority" && strings.Contains(item.ID, "Integration:99") {
			t.Fatal("classic push restrictions exclude this actor on main")
		}
	}
}
