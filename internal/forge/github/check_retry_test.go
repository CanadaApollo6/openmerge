package github

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// All requests, including workflow reconciliation and job-attempt reads, hit
// the local HTTP fixture. The journal represents the caller's committed state.
type rerunAPI struct {
	api            *apiFixture
	sha, ref       string
	workflows      map[uint64]*gh.WorkflowRun
	posts          map[uint64]int
	journal        []forge.RerunTarget
	post           func(http.ResponseWriter, *http.Request, uint64)
	loseAcceptance bool
}

func newRerunAPI(t *testing.T, ids ...uint64) *rerunAPI {
	t.Helper()
	f := newAPI(t)
	a := &rerunAPI{api: f, sha: "retry-sha", ref: "openmerge/queue/main/retry", workflows: map[uint64]*gh.WorkflowRun{}, posts: map[uint64]int{}}
	for _, id := range ids {
		f.runs[a.sha] = append(f.runs[a.sha], &gh.CheckRun{ID: new(int64(id + 1)), Name: new(fmt.Sprintf("ci-%d", id)), HeadSHA: &a.sha, DetailsURL: new(fmt.Sprintf("https://github.test/acme/project/actions/runs/%d/job/%d", id, id+1)), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: new(githubActionsAppID)}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}})
		a.workflows[id] = &gh.WorkflowRun{ID: new(int64(id)), HeadSHA: &a.sha, HeadBranch: &a.ref, Status: new("completed"), RunAttempt: new(1)}
	}
	f.suites[a.sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &a.ref}}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		prefix := "/api/v3/repos/acme/project/actions/runs/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
		id, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil || a.workflows[id] == nil {
			t.Errorf("unexpected workflow URL %s", r.URL.Path)
			w.WriteHeader(404)
			return true
		}
		switch {
		case r.Method == http.MethodGet && len(parts) == 1:
			f.json(w, a.workflows[id])
		case r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "jobs":
			if r.URL.Query().Get("filter") != "all" {
				t.Error("job attempt lookup must include retained attempts")
			}
			f.json(w, &gh.Jobs{Jobs: []*gh.WorkflowJob{{CheckRunURL: new(fmt.Sprintf("https://github.test/api/v3/repos/acme/project/check-runs/%d", id+1)), RunAttempt: new(int64(1))}}})
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "rerun-failed-jobs":
			a.posts[id]++
			if !slices.ContainsFunc(a.journal, func(target forge.RerunTarget) bool { return target.WorkflowRunID == id && !target.Accepted }) {
				t.Error("POST preceded durable intent")
			}
			if a.post != nil {
				a.post(w, r, id)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		default:
			t.Errorf("unexpected Actions request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
		return true
	}
	return a
}
func (a *rerunAPI) request(t *testing.T) forge.RerunChecksRequest {
	t.Helper()
	runs, err := a.api.driver.CheckRuns(t.Context(), testRepo, a.sha)
	must(t, err)
	return forge.RerunChecksRequest{TestedSHA: a.sha, TestRef: a.ref, Runs: runs, Key: "batch/retry", Targets: slices.Clone(a.journal), Progress: func(target forge.RerunTarget) error {
		if target.Accepted && a.loseAcceptance {
			return errors.New("process died before acceptance commit")
		}
		index := slices.IndexFunc(a.journal, func(old forge.RerunTarget) bool { return old.WorkflowRunID == target.WorkflowRunID })
		if index < 0 {
			a.journal = append(a.journal, target)
		} else {
			a.journal[index] = target
		}
		return nil
	}}
}
func (a *rerunAPI) restart() {
	a.api.driver = &Driver{app: a.api.app, installation: a.api.driver.installation, client: a.api.driver.client}
}
func TestRerunActionsUsesWorkflowRunIDAndIsIdempotent(t *testing.T) {
	a := newRerunAPI(t, 700)
	for range 2 {
		result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
		if err != nil || result.Unsupported {
			t.Fatalf("rerun=%+v, %v", result, err)
		}
	}
	if a.posts[700] != 1 || len(a.journal) != 1 || !a.journal[0].Accepted {
		t.Fatalf("posts=%v progress=%+v", a.posts, a.journal)
	}
}
func TestRerunLostResponseReconcilesWithoutAnotherPOST(t *testing.T) {
	a := newRerunAPI(t, 700)
	a.post = func(w http.ResponseWriter, _ *http.Request, id uint64) {
		a.workflows[id].RunAttempt = new(2)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}
	_, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err == nil {
		t.Fatal("lost response succeeded")
	}
	a.restart()
	result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err != nil || result.Unsupported || a.posts[700] != 1 || !a.journal[0].Accepted {
		t.Fatalf("reconcile=%+v %v posts=%v progress=%+v", result, err, a.posts, a.journal)
	}
}
func TestRerunCrashAfterAcceptanceWaitsForPositiveEvidence(t *testing.T) {
	a := newRerunAPI(t, 700)
	a.loseAcceptance = true
	_, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err == nil {
		t.Fatal("expected lost commit")
	}
	a.restart()
	a.loseAcceptance = false
	result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err == nil || result.Unsupported || a.posts[700] != 1 {
		t.Fatalf("unchanged original authorized POST: %+v %v %v", result, err, a.posts)
	}
	a.workflows[700].Status = new("queued")
	result, err = a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err != nil || result.Unsupported || a.posts[700] != 1 {
		t.Fatalf("reconcile=%+v %v %v", result, err, a.posts)
	}
}
func TestRerunPartialDispatchPersistsEachTarget(t *testing.T) {
	a := newRerunAPI(t, 700, 800)
	a.post = func(w http.ResponseWriter, _ *http.Request, id uint64) {
		if id == 800 {
			a.api.apiError(w, 500, "upstream failed")
		} else {
			w.WriteHeader(201)
		}
	}
	_, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err == nil {
		t.Fatal("expected 500")
	}
	if len(a.journal) != 2 || !a.journal[0].Accepted || a.journal[1].Accepted {
		t.Fatalf("partial progress=%+v", a.journal)
	}
	a.restart()
	result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err == nil || result.Unsupported || a.posts[700] != 1 || a.posts[800] != 1 {
		t.Fatalf("partial retry=%+v %v posts=%v", result, err, a.posts)
	}
	a.workflows[800].RunAttempt = new(2)
	result, err = a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
	if err != nil || result.Unsupported || a.posts[700] != 1 || a.posts[800] != 1 {
		t.Fatalf("reconciled=%+v %v posts=%v", result, err, a.posts)
	}
}
func TestRerunOnlyPermissionRefusalIsUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        int
		message     string
		unsupported bool
	}{
		{"permission", 403, "Resource not accessible by integration ghp_privatecredential", true},
		{"in progress", 403, "This workflow is already running", false},
		{"too old", 422, "This workflow is too old to rerun", false},
		{"missing", 404, "Not Found", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newRerunAPI(t, 700)
			a.post = func(w http.ResponseWriter, _ *http.Request, _ uint64) { a.api.apiError(w, tc.code, tc.message) }
			result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
			if result.Unsupported != tc.unsupported || (err == nil) != tc.unsupported {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.unsupported && (!strings.Contains(result.Detail, "Resource not accessible by integration") || strings.Contains(result.Detail, "ghp_privatecredential")) {
				t.Fatalf("unsanitized/missing message: %q", result.Detail)
			}
			if !tc.unsupported {
				a.workflows[700].RunAttempt = new(2)
				result, err = a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
				if err != nil || result.Unsupported || a.posts[700] != 1 {
					t.Fatalf("refusal reconcile=%+v %v %v", result, err, a.posts)
				}
			}
		})
	}
}
func TestRerunRateLimit403RemainsRetryable(t *testing.T) {
	for _, secondary := range []bool{false, true} {
		t.Run(fmt.Sprint(secondary), func(t *testing.T) {
			a := newRerunAPI(t, 700)
			a.post = func(w http.ResponseWriter, _ *http.Request, _ uint64) {
				message := "API rate limit exceeded"
				if secondary {
					w.Header().Set("Retry-After", "60")
					message = "secondary rate limit"
				} else {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				a.api.apiError(w, 403, message)
			}
			result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, a.request(t))
			var rate *RateLimitError
			if result.Unsupported || !errors.As(err, &rate) {
				t.Fatalf("rate limit=%+v %v", result, err)
			}
		})
	}
}
func TestRerunOnlyActionsWithValidatedURLAreSupported(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		app           int64
		status        bool
	}{
		{name: "wrong host", details: "https://evil.test/acme/project/actions/runs/700/job/701", app: 15368},
		{name: "wrong repo", details: "https://github.test/other/project/actions/runs/700/job/701", app: 15368},
		{name: "foreign app", details: "https://github.test/acme/project/actions/runs/700/job/701", app: 99},
		{name: "commit status", status: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newRerunAPI(t, 700)
			a.api.runs[a.sha][0].DetailsURL = &tc.details
			a.api.runs[a.sha][0].App.ID = &tc.app
			req := a.request(t)
			if tc.status {
				req.Runs = []forge.CheckRun{{ID: 1, Attempt: 1, Name: "status", Source: "status:ci", State: forge.Failure}}
			}
			result, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, req)
			if err != nil || !result.Unsupported || a.posts[700] != 0 {
				t.Fatalf("unsupported=%+v %v posts=%v", result, err, a.posts)
			}
		})
	}
}
func TestRerunIntentMustCommitBeforePOST(t *testing.T) {
	a := newRerunAPI(t, 700)
	req := a.request(t)
	req.Progress = func(forge.RerunTarget) error { return errors.New("database unavailable") }
	_, err := a.api.driver.RerunFailedChecks(t.Context(), testRepo, req)
	if err == nil || a.posts[700] != 0 {
		t.Fatalf("failed intent: %v posts=%v", err, a.posts)
	}
}
func TestRetryJobAttemptsDoNotAdvanceRetainedCheckIDs(t *testing.T) {
	a := newRerunAPI(t, 700)
	oldOverride := a.api.override
	a.api.runs[a.sha] = append(a.api.runs[a.sha], &gh.CheckRun{ID: new(int64(702)), Name: new("ci-700"), HeadSHA: &a.sha, DetailsURL: new("https://github.test/acme/project/actions/runs/700/job/702"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(githubActionsAppID)}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}})
	a.api.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/700/jobs") {
			a.api.json(w, &gh.Jobs{Jobs: []*gh.WorkflowJob{
				{CheckRunURL: new("https://github.test/repos/acme/project/check-runs/701"), RunAttempt: new(int64(1))},
				{CheckRunURL: new("https://github.test/repos/acme/project/check-runs/702"), RunAttempt: new(int64(2))},
			}})
			return true
		}
		return oldOverride(w, r)
	}
	checks, err := a.api.driver.CheckRunsForRetry(t.Context(), testRepo, a.sha)
	must(t, err)
	if len(checks) != 2 || checks[0].Attempt != 1 || checks[1].Attempt != 2 || checks[1].WorkflowRunID != 700 || checks[0].URL == "" || checks[0].WorkflowRunURL != "https://github.test/acme/project/actions/runs/700" {
		t.Fatalf("attempts/evidence=%+v", checks)
	}
}
