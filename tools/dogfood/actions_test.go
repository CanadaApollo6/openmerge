package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"
)

type fakeActions struct {
	runs         []*github.WorkflowRun
	historical   map[[2]int64]*github.WorkflowRun
	err          error
	calls        int
	attemptCalls [][2]int64
}

func (a *fakeActions) List(_ context.Context, sha string) ([]*github.WorkflowRun, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	var result []*github.WorkflowRun
	for _, r := range a.runs {
		if sha == "" || r.GetHeadSHA() == sha {
			result = append(result, r)
		}
	}
	return result, nil
}
func (a *fakeActions) Attempt(_ context.Context, id int64, n int) (*github.WorkflowRun, error) {
	a.attemptCalls = append(a.attemptCalls, [2]int64{id, int64(n)})
	r, ok := a.historical[[2]int64{id, int64(n)}]
	if !ok {
		for _, run := range a.runs {
			if run.GetID() == id && run.GetRunAttempt() == n {
				r, ok = run, true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("missing fake attempt %d/%d", id, n)
	}
	return r, nil
}
func runFixture(id int64, n, created, started, completed int, sha string) *github.WorkflowRun {
	return &github.WorkflowRun{ID: new(id), Name: new("CI"), Path: new(".github/workflows/ci.yml"), HeadBranch: new("openmerge/queue/main/b1"), HeadSHA: new(sha), RunAttempt: new(n), CreatedAt: &github.Timestamp{Time: epoch.Add(time.Duration(created) * time.Second)}, RunStartedAt: &github.Timestamp{Time: epoch.Add(time.Duration(started) * time.Second)}, UpdatedAt: &github.Timestamp{Time: epoch.Add(time.Duration(completed) * time.Second)}, Status: new("completed"), Conclusion: new("success")}
}
func TestRunAttemptDedupAndBatchAttributionAcrossBoundary(t *testing.T) {
	first := runFixture(1, 2, 10, 120, 130, "old")
	second := runFixture(2, 1, 80, 81, 90, "new")
	second.HeadBranch = new("openmerge/queue/main/b2")
	discarded := runFixture(3, 1, 200, 210, 220, "discarded")
	discarded.Conclusion = new("failure")
	other := runFixture(4, 1, 10, 11, 12, "other")
	other.HeadBranch = new("main")
	wrongBase := runFixture(5, 1, 10, 11, 12, "other-base")
	wrongBase.HeadBranch = new("openmerge/queue/release/x/b1")
	a := &fakeActions{runs: []*github.WorkflowRun{first, first, second, discarded, other, wrongBase}, historical: map[[2]int64]*github.WorkflowRun{{1, 1}: runFixture(1, 1, 10, 11, 20, "old")}}
	runs, err := countRuns(t.Context(), a, "main", []string{"b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[1].Number != 2 || runs[2].Conclusion != "failure" {
		t.Fatalf("A attempts: %+v", runs)
	}
	// Even the discarded batch attempt at t=210, after an earlier apparent
	// drain and B's boundary, belongs to A. Time never assigns it to B.
	after, err := countRuns(t.Context(), a, "main", []string{"b2"})
	if err != nil || len(after) != 1 || after[0].ID != 2 {
		t.Fatalf("B attempts: %+v, %v", after, err)
	}
	counts := map[[2]int64]int{}
	for _, key := range a.attemptCalls {
		counts[key]++
	}
	for _, key := range [][2]int64{{1, 1}, {1, 2}, {2, 1}, {3, 1}} {
		if counts[key] != 1 {
			t.Fatalf("attempt endpoint calls: %v", counts)
		}
	}
	if counts[[2]int64{4, 1}] != 0 || counts[[2]int64{5, 1}] != 0 {
		t.Fatal("nonqueue run expanded")
	}
}
func TestActionsAttemptErrorsAreNotSilentlyDropped(t *testing.T) {
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 2, 0, 1, 2, "sha")}}
	if _, err := countRuns(t.Context(), a, "main", []string{"b1"}); err == nil {
		t.Fatal("missing historical attempt accepted")
	}
	r := runFixture(1, 2, 0, 1, 2, "sha")
	r.UpdatedAt = nil
	a.historical = map[[2]int64]*github.WorkflowRun{{1, 1}: runFixture(1, 1, 0, 1, 2, "sha"), {1, 2}: r}
	if _, err := countRuns(t.Context(), a, "main", []string{"b1"}); err == nil {
		t.Fatal("missing completion timestamp accepted")
	}
}
func TestGitHubActionsPaginationAndAttemptTransport(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.URL.Path == "/repos/o/r/actions/runs/1/attempts/2" {
			_, _ = fmt.Fprint(w, `{"id":1,"run_attempt":2}`)
			return
		}
		if r.URL.Path != "/repos/o/r/actions/runs" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request: %s", r.URL)
			http.Error(w, "wrong request", 400)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/o/r/actions/runs?per_page=100&page=2>; rel="next"`, r.Host))
			_, _ = fmt.Fprint(w, `{"total_count":2,"workflow_runs":[{"id":1}]}`)
		} else {
			_, _ = fmt.Fprint(w, `{"total_count":2,"workflow_runs":[{"id":2}]}`)
		}
	}))
	defer server.Close()
	base := server.URL + "/"
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	a := githubActions{client: client, owner: "o", repo: "r"}
	runs, err := a.List(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatal(runs)
	}
	attempt, err := a.Attempt(t.Context(), 1, 2)
	if err != nil || attempt.GetRunAttempt() != 2 {
		t.Fatalf("attempt=%v err=%v", attempt, err)
	}
	if len(requests) != 3 || strings.Contains(requests[0], "created=") || strings.Contains(requests[0], "branch=") {
		t.Fatal(requests)
	}
}
func TestGitHubActionsRejectsTruncatedSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"total_count":1001,"workflow_runs":[]}`)
	}))
	defer server.Close()
	base := server.URL + "/"
	client, err := github.NewClient(github.WithHTTPClient(server.Client()), github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	a := githubActions{client: client, owner: "o", repo: "r"}
	if _, err := a.List(t.Context(), "sha"); err == nil {
		t.Fatal("truncated search accepted")
	}
}
