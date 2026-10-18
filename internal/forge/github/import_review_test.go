package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReviewReadOnlyBoundary(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			targetCalls := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetCalls++
				_, _ = io.WriteString(w, "{}")
			}))
			defer target.Close()
			originCalls := 0
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originCalls++
				http.Redirect(w, r, target.URL+"/destination", code)
			}))
			defer origin.Close()
			c, err := setupClient(origin.URL+"/", "test-token")
			if err != nil {
				t.Fatal(err)
			}
			c.http.Transport = getOnlyTransport{base: c.http.Transport}
			var result any
			for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE"} {
				if err := c.request(t.Context(), method, "repos/acme/project", map[string]string{"name": "changed"}, &result); err == nil {
					t.Errorf("allowed %s", method)
				}
			}
			for _, query := range []string{"query { viewer { login } }", "mutation { deleteRepository(input: {repositoryId: \"x\"}) { clientMutationId } }"} {
				if err := c.request(t.Context(), "POST", "graphql", map[string]string{"query": query}, &result); err == nil {
					t.Error("allowed GraphQL POST")
				}
			}
			if originCalls != 0 {
				t.Fatalf("non-GETs reached origin: %d", originCalls)
			}
			if err := c.request(t.Context(), "GET", "redirect", nil, &result); err == nil {
				t.Error("redirect accepted")
			}
			if originCalls != 1 || targetCalls != 0 {
				t.Fatalf("origin=%d target=%d", originCalls, targetCalls)
			}
		})
	}
	innerCalls := 0
	guard := getOnlyTransport{base: roundTripFunc(func(_ *http.Request) (*http.Response, error) { innerCalls++; return nil, fmt.Errorf("inner reached") })}
	req, _ := http.NewRequest("POST", "http://127.0.0.1/graphql", nil)
	_, _ = guard.RoundTrip(req)
	if innerCalls != 0 {
		t.Fatal("inner authentication/cache transport reached")
	}
}

func TestReviewDirectoryOnlyCoverage(t *testing.T) {
	r := ProtectionReport{}
	r.coverage(parseImportOwners(".github/CODEOWNERS", "*/ @acme/maintainers\n"))
	for _, path := range []string{".openmerge.yml", ".openmerge.yaml", "CODEOWNERS"} {
		item := findImportItem(t, r, "", "governing-CODEOWNERS", "coverage-"+path)
		if !item.Blocking {
			t.Errorf("directory-only pattern incorrectly clears file coverage: %+v", item)
		}
	}
}

func TestReviewStaleOwnerOnlyReviews(t *testing.T) {
	r := ProtectionReport{}
	err := r.translateRule("main", "ruleset:1", RepositoryRule{Type: "pull_request", Parameters: json.RawMessage(`{"required_approving_review_count":0,"require_code_owner_review":true,"dismiss_stale_reviews_on_push":true}`)}, []string{"alice"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.accept(nil); err != nil {
		t.Fatal(err)
	}
	if r.Ready {
		t.Errorf("owner-only stale-dismissal relaxation needs no acceptance: %+v", r.Items)
	}
}

func TestReviewOverlappingBypassPrincipals(t *testing.T) {
	r := ProtectionReport{}
	r.effectiveWriters("main", []Ruleset{
		{ID: 1, Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}},
		{ID: 2, Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "RepositoryRole", ActorID: new(int64(4)), BypassMode: "always"}}},
	}, nil)
	if len(r.Items) == 0 {
		t.Error("no authority blocker for a writer who is both a member of Team:7 and RepositoryRole:4")
	}
}

func TestReviewPerBaseWorkflows(t *testing.T) {
	f := newProtectionFixture(t)
	f.rules[0].Rules = []RepositoryRule{{Type: "required_status_checks", Parameters: json.RawMessage(`{"required_status_checks":[{"context":"ci"}]}`)}}
	f.files["release-tip"][".github/workflows/queue.yml"] = "on: pull_request\njobs:\n  ci:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := findImportItem(t, r, "release/1", "ruleset:11", "check-ci")
	if item.Classification != "unsupported" {
		t.Errorf("release PR-only workflow reported safe: %+v", item)
	}
}

func TestReviewReusableWorkflowUnknown(t *testing.T) {
	for _, input := range []string{"on: workflow_call", "on: [workflow_call]", "on: {workflow_call: {}}"} {
		var doc struct {
			On yaml.Node `yaml:"on"`
		}
		if err := yaml.Unmarshal([]byte(input), &doc); err != nil {
			t.Fatal(err)
		}
		queue, _ := workflowQueue(doc.On)
		if !strings.HasPrefix(queue, "unknown") {
			t.Errorf("%s: coverage=%q without examining caller", input, queue)
		}
	}
}

func TestReviewUnknownProducerCollision(t *testing.T) {
	f := newProtectionFixture(t)
	f.rules[0].Rules = []RepositoryRule{{Type: "required_status_checks", Parameters: json.RawMessage(`{"required_status_checks":[{"context":"ci"}]}`)}}
	f.files["main-tip"][".github/workflows/dynamic.yml"] = "on: push\njobs:\n  other:\n    name: ${{ 'ci' }}\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	r, err := fixtureReport(t, f, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := findImportItem(t, r, "main", "ruleset:11", "check-ci")
	if item.Classification == "equivalent" {
		t.Errorf("dynamic producer collision reported equivalent: %+v", item)
	}
}

func TestReviewRulesetTargetsAndPages(t *testing.T) {
	for _, tc := range []struct {
		conditions    string
		main, release bool
	}{
		{`{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}}`, true, false},
		{`{"ref_name":{"include":["~ALL"],"exclude":["refs/heads/release/*"]}}`, true, false},
		{`{"ref_name":{"include":["refs/heads/release/**/*"],"exclude":["~DEFAULT_BRANCH"]}}`, false, true},
		{`{"ref_name":{"include":["~ALL"],"exclude":["~ALL"]}}`, false, false},
	} {
		f := newProtectionFixture(t)
		f.rules[0].Conditions = json.RawMessage(tc.conditions)
		r, err := fixtureReport(t, f, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for base, want := range map[string]bool{"main": tc.main, "release/1": tc.release} {
			found := false
			for _, item := range r.Items {
				if item.Base == base && item.Source == "ruleset:11" && strings.HasPrefix(item.ID, "reviews-") {
					found = true
				}
			}
			if found != want {
				t.Errorf("conditions=%s base=%s found=%t want=%t", tc.conditions, base, found, want)
			}
		}
	}
	pages := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		pages[key]++
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", `<https://not-followed.invalid/?page=2>; rel="next"`)
		}
		if strings.HasSuffix(key, "check-runs") {
			_, _ = io.WriteString(w, fmt.Sprintf(`{"check_runs":[{"name":"ci","app":{"id":%d}}]}`, pages[key]))
		} else {
			_, _ = io.WriteString(w, fmt.Sprintf(`[{"context":"status","creator":{"id":%d}}]`, pages[key]))
		}
	}))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = getOnlyTransport{base: c.http.Transport}
	evidence, err := c.importChecks(context.Background(), "acme/project", "tip")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence["ci"]) != 2 || len(evidence["status"]) != 2 {
		t.Fatalf("lost pages: %v", evidence)
	}
}

func TestReviewFullPagination(t *testing.T) {
	for _, deniedSecondPage := range []bool{false, true} {
		t.Run(fmt.Sprint(deniedSecondPage), func(t *testing.T) {
			f := newProtectionFixture(t)
			original := f.server(t)
			defer original.Close()
			seen := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/repos/acme/project")
				if path != "/rulesets" && path != "/branches" && path != "/collaborators" {
					original.Config.Handler.ServeHTTP(w, r)
					return
				}
				page := r.URL.Query().Get("page")
				seen[path+"/"+page] = true
				if page == "1" {
					w.Header().Set("Link", `<https://not-followed.invalid/?page=2>; rel="next"`)
				} else if page != "2" {
					t.Errorf("unexpected page %q", page)
				}
				if deniedSecondPage && path == "/rulesets" && page == "2" {
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `{"message":"Forbidden"}`)
					return
				}
				var value any
				switch path {
				case "/rulesets":
					if page == "1" {
						value = f.rules[:1]
					} else {
						value = f.rules[1:]
					}
				case "/branches":
					if page == "1" {
						value = []any{map[string]any{"name": "main", "commit": map[string]string{"sha": "main-tip"}}}
					} else {
						value = []any{map[string]any{"name": "release/1", "commit": map[string]string{"sha": "release-tip"}}}
					}
				case "/collaborators":
					login := "alice"
					if page == "2" {
						login = "carol"
					}
					value = []any{map[string]any{"login": login, "permissions": map[string]bool{"push": true}}}
				}
				if err := json.NewEncoder(w).Encode(value); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			r, err := importProtection(t.Context(), ImportOptions{Repo: "acme/project"}, "admin-test-token", server.URL+"/")
			if deniedSecondPage {
				if err == nil || len(r.Items) > 0 {
					t.Fatalf("partial report after page 2 failure: err=%v report=%+v", err, r)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/rulesets/2", "/branches/2", "/collaborators/2"} {
				if !seen[path] {
					t.Error("page not read: " + path)
				}
			}
			if !strings.Contains(r.ProposedConfig, "carol") {
				t.Error("second-page reviewer omitted")
			}
			_ = findImportItem(t, r, "release/1", "authority", "writer-Integration:99")
		})
	}
}
