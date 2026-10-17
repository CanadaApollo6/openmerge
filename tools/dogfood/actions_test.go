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
	runs       []*github.WorkflowRun
	historical map[[2]int64]*github.WorkflowRun
	err        error
	calls      int
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
	r, ok := a.historical[[2]int64{id, int64(n)}]
	if !ok {
		return nil, fmt.Errorf("missing fake attempt %d/%d", id, n)
	}
	return r, nil
}
func runFixture(id int64, n, created, started, completed int, sha string) *github.WorkflowRun {
	return &github.WorkflowRun{ID: new(id), Name: new("CI"), HeadBranch: new("openmerge/queue/main/b1"), HeadSHA: new(sha), RunAttempt: new(n), CreatedAt: &github.Timestamp{Time: epoch.Add(time.Duration(created) * time.Second)}, RunStartedAt: &github.Timestamp{Time: epoch.Add(time.Duration(started) * time.Second)}, UpdatedAt: &github.Timestamp{Time: epoch.Add(time.Duration(completed) * time.Second)}, Status: new("completed"), Conclusion: new("success")}
}
func TestRunAttemptDedupAndRerunsAcrossWindows(t *testing.T) {
	old := runFixture(1, 2, -100, 20, 30, "old")
	current := runFixture(2, 2, 10, 80, 90, "current")
	ordinary := runFixture(3, 1, 40, 42, 50, "third")
	otherWorkflow := runFixture(4, 1, 20, 20, 30, "fourth")
	otherWorkflow.Name = new("Other")
	main := runFixture(5, 1, 20, 20, 30, "fifth")
	main.HeadBranch = new("main")
	outside := runFixture(6, 1, 100, 100, 110, "sixth")
	failed := runFixture(7, 1, 30, 30, 40, "invalidated")
	failed.Conclusion = new("failure")
	a := &fakeActions{runs: []*github.WorkflowRun{old, old, current, current, ordinary, otherWorkflow, main, outside, failed}, historical: map[[2]int64]*github.WorkflowRun{
		{1, 1}: runFixture(1, 1, -100, -99, -90, "old"), {1, 2}: old,
		{2, 1}: runFixture(2, 1, 10, 11, 15, "current"), {2, 2}: current,
	}}
	runs, err := countRuns(t.Context(), a, testWindow(0, 60))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 4 {
		t.Fatalf("expected old rerun, current original, ordinary and failed: %+v", runs)
	}
	keys := map[[2]int64]bool{}
	for _, r := range runs {
		keys[[2]int64{r.ID, int64(r.Number)}] = true
	}
	if !keys[[2]int64{1, 2}] || !keys[[2]int64{2, 1}] || !keys[[2]int64{7, 1}] {
		t.Fatal(keys)
	}
	after, err := countRuns(t.Context(), a, testWindow(60, 100))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != 2 || after[0].Number != 2 {
		t.Fatal(after)
	}
}
func TestActionsAttemptErrorsAreNotSilentlyDropped(t *testing.T) {
	a := &fakeActions{runs: []*github.WorkflowRun{runFixture(1, 2, 0, 1, 2, "sha")}}
	if _, err := countRuns(t.Context(), a, testWindow(0, 60)); err == nil {
		t.Fatal("missing historical attempt accepted")
	}
	r := runFixture(1, 2, 0, 1, 2, "sha")
	r.RunStartedAt = nil
	a.historical = map[[2]int64]*github.WorkflowRun{{1, 1}: runFixture(1, 1, 0, 1, 2, "sha"), {1, 2}: r}
	if _, err := countRuns(t.Context(), a, testWindow(0, 60)); err == nil {
		t.Fatal("missing rerun timestamp accepted")
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
	if len(requests) != 3 || strings.Contains(requests[0], "created=") {
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
