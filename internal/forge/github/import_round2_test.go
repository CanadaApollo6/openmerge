package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

func TestRound2TeamPages(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				pages++
				if req.URL.Query().Get("page") == "1" {
					w.Header().Set("Link", `<https://not-followed.invalid/?page=2>; rel="next"`)
					_, _ = fmt.Fprint(w, `[{"id":2,"login":"bob"}]`)
					return
				}
				if denied {
					w.WriteHeader(403)
					_, _ = fmt.Fprint(w, `{"message":"Forbidden"}`)
					return
				}
				_, _ = fmt.Fprint(w, `[{"id":1,"login":"alice"}]`)
			}))
			defer server.Close()
			c, err := setupClient(server.URL+"/", "test")
			if err != nil {
				t.Fatal(err)
			}
			r := ProtectionReport{principals: map[string][]string{"RepositoryRole:4": {"User:1"}}}
			gates := []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}}, {Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "RepositoryRole", ActorID: new(int64(4)), BypassMode: "always"}}}}
			c.importPrincipals(t.Context(), &r, gates, nil)
			r.effectiveWriters("main", gates, nil)
			key := "principal-User:1"
			if denied {
				key = "unresolved-memberships"
			}
			item := findImportItem(t, r, "main", "authority", key)
			if !item.Blocking || pages != 2 {
				t.Fatalf("pages=%d item=%+v", pages, item)
			}
		})
	}
}

func TestRound2OrgRoleAndAdmin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":1,"login":"maintainer","role_name":"maintain","permissions":{"maintain":true,"push":true}},{"id":2,"login":"admin","role_name":"admin","permissions":{"admin":true,"maintain":true,"push":true}}]`)
	}))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := ProtectionReport{}
	_, err = c.importWriters(t.Context(), "acme/project", &r)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r.principals["RepositoryRole:5"]) != "[User:2]" {
		t.Fatal(r.principals)
	}
	r.effectiveWriters("main", []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "OrganizationAdmin", BypassMode: "always"}}}}, nil)
	if !findImportItem(t, r, "main", "authority", "unresolved-memberships").Blocking {
		t.Fatal("org role not blocking")
	}
}

func TestRound2ClassicAdminBypass(t *testing.T) {
	r := ProtectionReport{principals: map[string][]string{"Team:7": {"User:1"}, "RepositoryRole:5": {"User:1"}}}
	gates := []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}}}
	p := &importClassicProtection{Admins: importToggle{Enabled: false}, Restrictions: &classicActors{Users: []classicIdentity{{ID: 2, Login: "bob"}}}}
	r.effectiveWriters("main", gates, p)
	if len(r.Items) == 0 {
		t.Fatal("admin User:1 bypasses classic restrictions and belongs to ruleset bypass Team:7, but authority report has no items")
	}
}

func TestRound2EmptyDiff(t *testing.T) {
	r := ProtectionReport{}
	r.reviews("main", "ruleset:1", importReviews{Count: 3}, []string{"alice", "bob", "carol"})
	if err := r.accept(nil); err != nil {
		t.Fatal(err)
	}
	g, err := condition.CompileGlob("**")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"README.md", "src/main.go", ".github/workflows/ci.yml"} {
		if !g.MatchString(path) {
			t.Fatal(path)
		}
	}
	files := []string{}
	matches := false
	for _, path := range files {
		matches = matches || g.MatchString(path)
	}
	if !matches && !strings.Contains(r.ProposedConfig, "default:\n        count: 3\n        group: imported_reviewers") {
		t.Fatal("known empty diff must retain the count-bearing default alongside touches **")
	}
}

func TestRound2ConditionalJob(t *testing.T) {
	f := newProtectionFixture(t)
	f.files["main-tip"][".github/workflows/queue.yml"] = "on: push\njobs:\n  ci:\n    if: github.event_name == 'pull_request'\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	server := f.server(t)
	defer server.Close()
	c, err := setupClient(server.URL+"/", "admin-test-token")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := c.importTree(t.Context(), "acme/project", "main-tip")
	if err != nil {
		t.Fatal(err)
	}
	workflows, err := c.importWorkflows(t.Context(), "acme/project", tree)
	if err != nil {
		t.Fatal(err)
	}
	r := ProtectionReport{}
	r.requiredChecks("main", "test", importStatusChecks{Contexts: []string{"ci"}}, checkEvidence{"ci": {"check:15368": true}}, workflows)
	item := findImportItem(t, r, "main", "test", "check-ci")
	if item.Classification == "equivalent" {
		t.Fatalf("job skipped on every queue push classified equivalent: %+v", item)
	}
}

func TestRound2RealWriteRoleID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":1,"login":"alice","role_name":"write","permissions":{"push":true}}]`)
	}))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := ProtectionReport{principals: map[string][]string{"Team:7": {"User:1"}}}
	_, err = c.importWriters(t.Context(), "acme/project", &r)
	if err != nil {
		t.Fatal(err)
	}
	gates := []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}}, {Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "RepositoryRole", ActorID: new(int64(4)), BypassMode: "always"}}}}
	r.effectiveWriters("main", gates, nil)
	if len(r.Items) == 0 {
		t.Fatalf("write-role User:1 belongs to Team:7 and RepositoryRole:4, but no authority items; principals=%v", r.principals)
	}
}

func TestRound2RoleIdentifiersAndUnknownRoles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":1,"login":"writer","role_name":"write","permissions":{"push":true}},{"id":2,"login":"maintainer","role_name":"maintain","permissions":{"push":true,"maintain":true}},{"id":3,"login":"admin","role_name":"admin","permissions":{"push":true,"admin":true}}]`)
	}))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := ProtectionReport{}
	if _, err := c.importWriters(t.Context(), "acme/project", &r); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64][]string{2: {"User:2", "User:3"}, 4: {"User:1", "User:2", "User:3"}, 5: {"User:3"}} {
		got, known := r.actorPrincipals(BypassActor{ActorType: "RepositoryRole", ActorID: &id})
		if !known || !slices.Equal(got, want) {
			t.Fatalf("role %d: got %v known=%t; want %v", id, got, known, want)
		}
	}
	for _, id := range []int64{1, 3, 42} {
		unknown := ProtectionReport{principals: r.principals}
		unknown.effectiveWriters("main", []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "RepositoryRole", ActorID: &id, BypassMode: "always"}}}}, nil)
		item := findImportItem(t, unknown, "main", "authority", "unresolved-memberships")
		if !item.Blocking {
			t.Fatal(item)
		}
		if err := unknown.accept([]string{item.ID}); err == nil {
			t.Fatal("unknown role accepted")
		}
	}
}

func TestRound2CustomCollaboratorRoleIsUnresolved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id":1,"login":"alice","role_name":"custom-maintainer","permissions":{"push":true,"maintain":true}}]`)
	}))
	defer server.Close()
	c, err := setupClient(server.URL+"/", "test")
	if err != nil {
		t.Fatal(err)
	}
	r := ProtectionReport{}
	if _, err := c.importWriters(t.Context(), "acme/project", &r); err != nil {
		t.Fatal(err)
	}
	r.effectiveWriters("main", []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "RepositoryRole", ActorID: new(int64(4)), BypassMode: "always"}}}}, nil)
	if !findImportItem(t, r, "main", "authority", "unresolved-memberships").Blocking {
		t.Fatal(r.Items)
	}
}

func TestRound2ClassicAdminIntersection(t *testing.T) {
	for _, enforced := range []bool{false, true} {
		for _, known := range []bool{false, true} {
			t.Run(fmt.Sprintf("enforced=%t/known=%t", enforced, known), func(t *testing.T) {
				r := ProtectionReport{principals: map[string][]string{"Team:7": {"User:1"}}}
				if known {
					r.principals["RepositoryRole:5"] = []string{"User:1"}
				}
				p := &importClassicProtection{Admins: importToggle{Enabled: enforced}, Restrictions: &classicActors{Users: []classicIdentity{{ID: 2}}}}
				r.effectiveWriters("main", []Ruleset{{Rules: []RepositoryRule{{Type: "update"}}, BypassActors: []BypassActor{{ActorType: "Team", ActorID: new(int64(7)), BypassMode: "always"}}}}, p)
				if enforced {
					if len(r.Items) != 0 {
						t.Fatalf("admin incorrectly exempt from enforced restrictions: %+v", r.Items)
					}
					return
				}
				key := "unresolved-memberships"
				if known {
					key = "principal-User:1"
				}
				if !findImportItem(t, r, "main", "authority", key).Blocking {
					t.Fatal(r.Items)
				}
			})
		}
	}
}

func TestRound2DefaultCountIncludesEmptyInventory(t *testing.T) {
	r := ProtectionReport{}
	r.reviews("main", "test", importReviews{Count: 3}, []string{"alice", "bob", "carol"})
	if err := r.accept(nil); err != nil {
		t.Fatal(err)
	}
	fragmentStart := strings.Index(r.ProposedConfig, "approvals:\n")
	if fragmentStart < 0 {
		t.Fatal("missing approvals")
	}
	var draft struct {
		Approvals struct {
			Default struct {
				Group string `yaml:"group"`
				Count int    `yaml:"count"`
			} `yaml:"default"`
			Rules []struct {
				When struct {
					Touches []string `yaml:"touches"`
				} `yaml:"when"`
				Require struct {
					Group string `yaml:"group"`
					Count int    `yaml:"count"`
				} `yaml:"require"`
			} `yaml:"rules"`
		} `yaml:"approvals"`
	}
	if err := yaml.Unmarshal([]byte(r.ProposedConfig[fragmentStart:]), &draft); err != nil {
		t.Fatal(err)
	}
	// No touches rule matches [], so Stage A applies the count-bearing default.
	if len(draft.Approvals.Rules) != 1 || draft.Approvals.Default.Count != 3 || draft.Approvals.Default.Group != "imported_reviewers" {
		t.Fatalf("lossy empty-inventory requirement: %+v", draft)
	}
	rule := draft.Approvals.Rules[0]
	if !slices.Equal(rule.When.Touches, []string{"**"}) || rule.Require.Count != 3 || rule.Require.Group != "imported_reviewers" {
		t.Fatalf("non-empty diffs must enforce the same count independently of default: %+v", rule)
	}
}

func TestRound2JobConditions(t *testing.T) {
	for _, value := range []string{"", "true", "${{ true }}", "false", "github.event_name == 'pull_request'", "github.event_name == 'push'", "always()", "${{ needs.build.result == 'success' }}"} {
		t.Run(value, func(t *testing.T) {
			f := newProtectionFixture(t)
			conditional := ""
			if value != "" {
				conditional = "    if: " + value + "\n"
			}
			f.files["main-tip"][".github/workflows/queue.yml"] = "on: push\njobs:\n  ci:\n" + conditional + "    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
			server := f.server(t)
			defer server.Close()
			c, err := setupClient(server.URL+"/", "admin-test-token")
			if err != nil {
				t.Fatal(err)
			}
			tree, err := c.importTree(t.Context(), "acme/project", "main-tip")
			if err != nil {
				t.Fatal(err)
			}
			workflows, err := c.importWorkflows(t.Context(), "acme/project", tree)
			if err != nil {
				t.Fatal(err)
			}
			r := ProtectionReport{}
			r.requiredChecks("main", "test", importStatusChecks{Contexts: []string{"ci"}}, checkEvidence{"ci": {"check:1": true}}, workflows)
			item := findImportItem(t, r, "main", "test", "check-ci")
			proven := value == "" || value == "true" || value == "${{ true }}"
			if (item.Classification == "equivalent") != proven {
				t.Fatalf("condition %q: %+v", value, item)
			}
		})
	}
}

func TestRound2UnknownConditionsCannotHideRules(t *testing.T) {
	for _, conditions := range []string{
		`{"ref_name":{"include":["~ALL"]},"repository_name":{"include":["project"],"exclude":[]}}`,
		`{"ref_name":{"include":["~ALL"],"future_condition":true}}`,
	} {
		f := newProtectionFixture(t)
		f.rules[0].Conditions = json.RawMessage(conditions)
		f.rules[0].Rules = []RepositoryRule{{Type: "pull_request", Parameters: json.RawMessage(`{"required_approving_review_count":3}`)}, {Type: "required_deployments", Parameters: json.RawMessage(`{"required_deployment_environments":["production"]}`)}}
		r, err := fixtureReport(t, f, ImportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := findImportItem(t, r, "main", "ruleset:11", "conditions")
		for _, text := range []string{conditions, "pull_request", "required_deployments", "production", "required_approving_review_count"} {
			if !strings.Contains(item.Message, text) {
				t.Fatalf("hidden requirement %s: %+v", text, item)
			}
		}
		if item.Classification != "unsupported" || !item.Blocking {
			t.Fatal(item)
		}
		if err := r.accept([]string{item.ID}); err == nil {
			t.Fatal("accepted hidden untranslated requirements")
		}
	}
}

func TestRound2UnhandledProtectionFields(t *testing.T) {
	for _, ruleset := range []bool{false, true} {
		for _, value := range []string{"true", "false", "null"} {
			t.Run(fmt.Sprintf("ruleset=%t/%s", ruleset, value), func(t *testing.T) {
				r := ProtectionReport{}
				raw := `{"required_status_checks":[{"context":"ci","future_binding":7}],"do_not_enforce_on_create":` + value + `}`
				if ruleset {
					if err := r.translateRule("main", "test", RepositoryRule{Type: "required_status_checks", Parameters: json.RawMessage(raw)}, nil, nil, nil); err != nil {
						t.Fatal(err)
					}
				} else {
					var p importClassicProtection
					if err := json.Unmarshal([]byte(`{"required_signatures":{"enabled":false},"required_pull_request_reviews":{"future_review":true},"required_status_checks":`+raw+`,"allow_fork_syncing":{"enabled":`+value+`},"enforce_admins":{"enabled":true,"future_admin":true}}`), &p); err != nil {
						t.Fatal(err)
					}
					if err := r.translateClassic("main", &p, nil, nil, nil); err != nil {
						t.Fatal(err)
					}
					for _, key := range []string{"allow_fork_syncing", "required_pull_request_reviews.future_review", "enforce_admins.future_admin"} {
						item := findImportItem(t, r, "main", "classic", "classic-field-"+key)
						if item.Classification != "unsupported" || !strings.Contains(item.Message, key) {
							t.Fatal(item)
						}
					}
				}
				source := "test"
				if !ruleset {
					source = "classic"
				}
				for _, key := range []string{"do_not_enforce_on_create", "required_status_checks[0].future_binding"} {
					item := findImportItem(t, r, "main", source, importIDPrefix("required-status-checks-parameter-"+key))
					if item.Classification != "unsupported" || !strings.Contains(item.Message, key) {
						t.Fatal(item)
					}
				}
			})
		}
	}
}

func TestRound2UpdateFetchAndMergeNamed(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		r := ProtectionReport{}
		if err := r.translateRule("main", "test", RepositoryRule{Type: "update", Parameters: json.RawMessage(fmt.Sprintf(`{"update_allows_fetch_and_merge":%t}`, enabled))}, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		key := "update"
		if enabled {
			key = "update_allows_fetch_and_merge"
		}
		item := findImportItem(t, r, "main", "test", key)
		if !strings.Contains(item.Message, "update_allows_fetch_and_merge") || enabled && item.Classification != "unsupported" {
			t.Fatal(item)
		}
	}
}

func TestRound2UnresolvedProducerScope(t *testing.T) {
	for _, job := range []string{
		"    name: lint-${{ matrix.os }}\n    strategy: {matrix: {os: [ubuntu-latest]}}\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n",
		"    name: lint\n    strategy: {matrix: {os: [ubuntu-latest]}}\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n",
	} {
		f := newProtectionFixture(t)
		f.files["main-tip"][".github/workflows/lint.yml"] = "on: push\njobs:\n  lint:\n" + job
		server := f.server(t)
		defer server.Close()
		c, err := setupClient(server.URL+"/", "admin-test-token")
		if err != nil {
			t.Fatal(err)
		}
		tree, err := c.importTree(t.Context(), "acme/project", "main-tip")
		if err != nil {
			t.Fatal(err)
		}
		workflows, err := c.importWorkflows(t.Context(), "acme/project", tree)
		if err != nil {
			t.Fatal(err)
		}
		r := ProtectionReport{}
		r.requiredChecks("main", "test", importStatusChecks{Contexts: []string{"ci", "lint-ubuntu", "lint (ubuntu-latest)"}}, checkEvidence{"ci": {"check:1": true}, "lint-ubuntu": {"check:1": true}, "lint (ubuntu-latest)": {"check:1": true}}, workflows)
		if findImportItem(t, r, "main", "test", "check-ci").Classification != "equivalent" {
			t.Fatal("unrelated unresolved job poisoned ci")
		}
		for _, name := range []string{"lint-ubuntu", "lint (ubuntu-latest)"} {
			if findImportItem(t, r, "main", "test", importIDPrefix("check-"+name)).Classification != "unsupported" {
				t.Fatal("unresolved producer declared safe")
			}
		}
	}
}

func TestRound2MissingOwnersUnsupported(t *testing.T) {
	r := ProtectionReport{}
	r.coverage(parseImportOwners("CODEOWNERS", "docs/ @alice\n"))
	item := findImportItem(t, r, "", "governing-CODEOWNERS", "coverage-.openmerge.yml")
	if item.Classification != "unsupported" || !item.Blocking {
		t.Fatal(item)
	}
}
