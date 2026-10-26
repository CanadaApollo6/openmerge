package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v93/github"
)

func TestGitHubMainEvidenceTransportAndFailures(t *testing.T) {
	tip, landed := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, test := range []struct {
		name, status string
		code         int
		want, fail   bool
	}{
		{"ahead", "ahead", 200, true, false},
		{"identical", "identical", 200, true, false},
		{"behind", "behind", 200, false, false},
		{"diverged", "diverged", 200, false, false},
		{"missing status", "", 200, false, true},
		{"unexpected status", "unknown", 200, false, true},
		{"unavailable", "", 503, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				w.WriteHeader(test.code)
				if strings.HasSuffix(r.URL.Path, "/branches/main") {
					_, _ = fmt.Fprintf(w, `{"commit":{"sha":%q}}`, tip)
				} else {
					_, _ = fmt.Fprintf(w, `{"status":%q}`, test.status)
				}
			}))
			defer server.Close()
			base := server.URL + "/"
			client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(&base, &base))
			if err != nil {
				t.Fatal(err)
			}
			a := githubActions{client: client, owner: "o", repo: "r"}
			got, err := a.BaseTip(t.Context(), "main")
			if test.code != 200 {
				if err == nil {
					t.Fatal("unavailable tip accepted")
				}
			} else if err != nil || got != tip {
				t.Fatalf("tip=%q err=%v", got, err)
			}
			ok, err := a.Ancestor(t.Context(), landed, tip)
			if (err != nil) != test.fail || ok != test.want {
				t.Fatalf("ancestor=%t err=%v", ok, err)
			}
			if len(paths) != 2 || paths[0] != "/repos/o/r/branches/main" || paths[1] != "/repos/o/r/compare/"+landed+"..."+tip {
				t.Fatal(paths)
			}
		})
	}
}
func TestAuditRejectsMainRewindAfterLastLanding(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("landed", base)
	g.git("", "update-ref", "refs/heads/main", base)
	f := auditFixture(t, "merge", base, tested, tested, tested)
	r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 20, tested)}})
	if !hasViolation(r, "main_membership") {
		t.Fatal("main rewind accepted", r)
	}
}
func TestWorkflowPathIdentityForAuditAndCounts(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("landed", base)
	f := auditFixture(t, "merge", base, tested, tested, tested)
	for _, wrongPath := range []bool{false, true} {
		run := runFixture(1, 1, 10, 11, 20, tested)
		if wrongPath {
			run.Path = new(".github/workflows/unrelated.yml")
		} else {
			run.Name = new("Renamed CI")
		}
		a := &fakeActions{runs: []*github.WorkflowRun{run}}
		r := auditTest(t, f, g, a)
		if hasViolation(r, "ci_success") != wrongPath {
			t.Fatal(r)
		}
		runs, err := countRuns(t.Context(), nil, a, "main", []string{"b1"})
		want := 1
		if wrongPath {
			want = 0
		}
		if err != nil || len(runs) != want {
			t.Fatalf("runs=%v err=%v", runs, err)
		}
	}
}
func TestAuditCIAtLandingStartedIsTooLate(t *testing.T) {
	g := newGitFixture(t)
	base := g.commit("base")
	tested := g.commit("landed", base)
	f := auditFixture(t, "merge", base, tested, tested, tested)
	r := auditTest(t, f, g, &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 1, 10, 11, 30, tested)}})
	if !hasViolation(r, "ci_success") {
		t.Fatal("CI at push deadline accepted", r)
	}
}
