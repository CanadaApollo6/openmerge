package github

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

type cancelAPI struct {
	*rerunAPI
	cancels int
	post    func(http.ResponseWriter, *http.Request)
}

func newCancelAPI(t *testing.T) *cancelAPI {
	t.Helper()
	a := &cancelAPI{rerunAPI: newRerunAPI(t, 700)}
	a.workflows[700].Status = new("in_progress")
	a.workflows[700].Conclusion = nil
	a.api.runs[a.sha][0].Status = new("in_progress")
	a.api.runs[a.sha][0].Conclusion = nil
	old := a.api.override
	a.api.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel") {
			a.cancels++
			if r.URL.Path != "/api/v3/repos/acme/project/actions/runs/700/cancel" {
				t.Errorf("unattributed cancellation: %s", r.URL.Path)
			}
			if a.post != nil {
				a.post(w, r)
			} else {
				w.WriteHeader(http.StatusAccepted)
			}
			return true
		}
		return old(w, r)
	}
	return a
}

func (a *cancelAPI) cancelRequest(t *testing.T) forge.CancelChecksRequest {
	t.Helper()
	runs, err := a.api.driver.CheckRuns(t.Context(), testRepo, a.sha)
	must(t, err)
	return forge.CancelChecksRequest{TestedSHA: a.sha, TestRef: a.ref, Runs: runs, Key: "batch/cancel"}
}

func TestCancelActionsRequestsOnlyRecordedWorkflowAndConfirmsOnObservation(t *testing.T) {
	a := newCancelAPI(t)
	req := a.cancelRequest(t)
	for range 2 {
		result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
		if err != nil || result.Unsupported != "" || len(result.Confirmed) != 0 {
			t.Fatalf("acceptance is not confirmation: %+v %v", result, err)
		}
		a.restart()
	}
	if a.cancels != 2 {
		t.Fatalf("idempotent repeated cancellation: %d", a.cancels)
	}
	for _, conclusion := range []string{"cancelled", "success", "failure"} {
		a.workflows[700].Status = new("completed")
		a.workflows[700].Conclusion = &conclusion
		a.api.runs[a.sha][0].Status = new("completed")
		a.api.runs[a.sha][0].Conclusion = &conclusion
		result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
		if err != nil || result.Unsupported != "" || len(result.Confirmed) != 1 || result.Confirmed[0].ID != 701 || result.Confirmed[0].State != checkState("completed", conclusion) || a.cancels != 2 {
			t.Fatalf("confirmation: %+v %v cancels=%d", result, err, a.cancels)
		}
	}
}

func TestCancelActionsCompletedWorkflowPreservesIndividualCheckOutcomes(t *testing.T) {
	a := newCancelAPI(t)
	a.workflows[700].Status = new("completed")
	a.workflows[700].Conclusion = new("failure")
	a.api.runs[a.sha] = nil
	for i, outcome := range []string{"success", "failure", ""} {
		status := "completed"
		if outcome == "" {
			status = "in_progress"
		}
		a.api.runs[a.sha] = append(a.api.runs[a.sha], &gh.CheckRun{
			ID: new(int64(701 + i)), Name: new([]string{"lint", "tests", "pending-job"}[i]), HeadSHA: &a.sha,
			DetailsURL: new("https://github.test/acme/project/actions/runs/700/job/" + []string{"701", "702", "703"}[i]),
			Status:     &status, Conclusion: &outcome, App: &gh.App{ID: new(githubActionsAppID)}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))},
		})
	}
	req := a.cancelRequest(t)
	result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported != "" || len(result.Confirmed) != 2 || result.Confirmed[0].State != forge.Success || result.Confirmed[1].State != forge.Failure || a.cancels != 0 {
		t.Fatalf("aggregate workflow outcome replaced individual job evidence: %+v %v posts=%d", result, err, a.cancels)
	}
	// A completed workflow does not invent an outcome for a stale pending job.
	a.api.runs[a.sha][2].Status = new("completed")
	a.api.runs[a.sha][2].Conclusion = new("cancelled")
	result, err = a.api.driver.CancelChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported != "" || len(result.Confirmed) != 3 || result.Confirmed[2].State != forge.Cancelled || a.cancels != 0 {
		t.Fatalf("terminal job evidence did not confirm completion: %+v %v posts=%d", result, err, a.cancels)
	}
}

func TestCancelActionsValidatesEveryWorkflowBeforeFirstPOST(t *testing.T) {
	a := newCancelAPI(t)
	a.api.runs[a.sha] = append(a.api.runs[a.sha], &gh.CheckRun{
		ID: new(int64(801)), Name: new("ci-800"), HeadSHA: &a.sha,
		DetailsURL: new("https://github.test/acme/project/actions/runs/800/job/801"),
		Status:     new("in_progress"), App: &gh.App{ID: new(githubActionsAppID)}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))},
	})
	a.workflows[800] = &gh.WorkflowRun{ID: new(int64(800)), HeadSHA: new("foreign-sha"), HeadBranch: &a.ref, Status: new("in_progress"), RunAttempt: new(1)}
	_, err := a.api.driver.CancelChecks(t.Context(), testRepo, a.cancelRequest(t))
	if err == nil || a.cancels != 0 {
		t.Fatalf("partially validated request mutated workflows: %v posts=%d", err, a.cancels)
	}
}

func TestCancelActionsDeduplicatesWorkflowTargets(t *testing.T) {
	a := newCancelAPI(t)
	req := a.cancelRequest(t)
	req.Runs = append(req.Runs, req.Runs[0])
	result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported != "" || a.cancels != 1 {
		t.Fatalf("workflow deduplication: %+v %v posts=%d", result, err, a.cancels)
	}
}

func TestCancelActionsRejectsUnattributedRequestsBeforeMutation(t *testing.T) {
	for _, variation := range []string{"sha", "ref", "source", "check id", "workflow id", "check branch", "workflow response id", "workflow sha", "workflow branch", "new attempt", "unknown attempt", "foreign URL"} {
		t.Run(variation, func(t *testing.T) {
			a := newCancelAPI(t)
			req := a.cancelRequest(t)
			switch variation {
			case "sha":
				req.TestedSHA = "another-sha"
			case "ref":
				req.TestRef = "another-ref"
			case "source":
				req.Runs[0].Source = "status:ci"
			case "check id":
				req.Runs[0].ID = 999
			case "workflow id":
				req.Runs[0].WorkflowRunID = 999
			case "check branch":
				req.Runs[0].HeadBranch = "main"
			case "workflow sha":
				a.workflows[700].HeadSHA = new("other-sha")
			case "workflow response id":
				a.workflows[700].ID = new(int64(999))
			case "workflow branch":
				a.workflows[700].HeadBranch = new("main")
			case "new attempt":
				a.workflows[700].RunAttempt = new(2)
			case "unknown attempt":
				a.workflows[700].RunAttempt = nil
			case "foreign URL":
				a.api.runs[a.sha][0].DetailsURL = new("https://evil.test/acme/project/actions/runs/700/job/701")
			}
			result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
			if err == nil && result.Unsupported == "" || a.cancels != 0 {
				t.Fatalf("unsafe request accepted: %+v %v cancels=%d", result, err, a.cancels)
			}
		})
	}
}

func TestCancelActionsPermissionFallbackAndOtherFailures(t *testing.T) {
	for _, code := range []int{403, 404, 409, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			a := newCancelAPI(t)
			a.post = func(w http.ResponseWriter, _ *http.Request) {
				a.api.apiError(w, code, "Resource not accessible by integration ghp_privatecredential")
			}
			result, err := a.api.driver.CancelChecks(t.Context(), testRepo, a.cancelRequest(t))
			if code == 403 {
				if err != nil || result.Unsupported == "" || strings.Contains(result.Unsupported, "ghp_privatecredential") {
					t.Fatalf("permission fallback: %+v %v", result, err)
				}
			} else if code == 409 {
				if err != nil || result.Unsupported != "" || len(result.Confirmed) > 0 {
					t.Fatalf("raced completion: %+v %v", result, err)
				}
			} else if err == nil || result.Unsupported != "" {
				t.Fatalf("error became unsupported: %+v %v", result, err)
			}
		})
	}
	t.Run("read permission", func(t *testing.T) {
		a := newCancelAPI(t)
		old := a.api.override
		a.api.override = func(w http.ResponseWriter, r *http.Request) bool {
			if strings.Contains(r.URL.Path, "/actions/") {
				a.api.apiError(w, 403, "Resource not accessible by integration")
				return true
			}
			return old(w, r)
		}
		result, err := a.api.driver.CancelChecks(t.Context(), testRepo, a.cancelRequest(t))
		if err != nil || result.Unsupported == "" || a.cancels != 0 {
			t.Fatalf("read permission: %+v %v", result, err)
		}
	})
	t.Run("ordinary forbidden", func(t *testing.T) {
		a := newCancelAPI(t)
		a.post = func(w http.ResponseWriter, _ *http.Request) { a.api.apiError(w, 403, "ordinary forbidden") }
		result, err := a.api.driver.CancelChecks(t.Context(), testRepo, a.cancelRequest(t))
		if err == nil || result.Unsupported != "" {
			t.Fatalf("ordinary forbidden: %+v %v", result, err)
		}
	})
}

func TestCancelActionsRateLimit403RemainsRetryable(t *testing.T) {
	for _, secondary := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary", true: "secondary"}[secondary], func(t *testing.T) {
			a := newCancelAPI(t)
			a.post = func(w http.ResponseWriter, _ *http.Request) {
				if secondary {
					w.Header().Set("Retry-After", "60")
				} else {
					w.Header().Set("X-RateLimit-Remaining", "0")
				}
				a.api.apiError(w, 403, "rate limited")
			}
			req := a.cancelRequest(t)
			result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
			var rate *RateLimitError
			if result.Unsupported != "" || !errors.As(err, &rate) {
				t.Fatalf("quota: %+v %v", result, err)
			}
			a.restart()
			a.api.clock = a.api.clock.Add(2 * time.Hour)
			a.post = nil
			result, err = a.api.driver.CancelChecks(t.Context(), testRepo, req)
			if err != nil || result.Unsupported != "" || a.cancels != 2 {
				t.Fatalf("retry: %+v %v posts=%d", result, err, a.cancels)
			}
		})
	}
}

func TestCancelActionsLostResponseCanSafelyRepeatAfterRestart(t *testing.T) {
	a := newCancelAPI(t)
	req := a.cancelRequest(t)
	tr := a.api.driver.client.Client().Transport.(*transport)
	base := tr.base
	tr.base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		res, err := base.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel") {
			_ = res.Body.Close()
			return nil, errors.New("lost cancellation response")
		}
		return res, err
	})
	if _, err := a.api.driver.CancelChecks(t.Context(), testRepo, req); err == nil {
		t.Fatal("lost response accepted")
	}
	tr.base = base
	a.restart()
	result, err := a.api.driver.CancelChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported != "" || len(result.Confirmed) > 0 || a.cancels != 2 {
		t.Fatalf("restart: %+v %v posts=%d", result, err, a.cancels)
	}
}
