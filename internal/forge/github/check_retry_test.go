package github

import (
	"net/http"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

func TestRerunActionsUsesWorkflowRunIDAndIsIdempotent(t *testing.T) {
	f := newAPI(t)
	sha := "retry-sha"
	ref := "openmerge/queue/main/retry"
	checkID := int64(701)
	f.runs[sha] = []*gh.CheckRun{{
		ID: &checkID, Name: new("ci"), HeadSHA: &sha, DetailsURL: new("https://github.test/acme/project/actions/runs/700/job/701"),
		Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: new(int64(15368))}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))},
	}}
	f.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &ref}}
	actionsCalls := 0
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/runs/700/rerun-failed-jobs") {
			actionsCalls++
			w.WriteHeader(http.StatusCreated)
			return true
		}
		return false
	}
	runs, err := f.driver.CheckRuns(t.Context(), testRepo, sha)
	must(t, err)
	if len(runs) != 1 || runs[0].Attempt != 1 {
		t.Fatalf("observed runs = %+v", runs)
	}
	req := forge.RerunChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: "batch/retry"}
	result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported {
		t.Fatalf("first rerun = %+v, %v", result, err)
	}
	result, err = f.driver.RerunFailedChecks(t.Context(), testRepo, req)
	if err != nil || result.Unsupported || actionsCalls != 1 {
		t.Fatalf("duplicate rerun = %+v, %v; calls=%d", result, err, actionsCalls)
	}
}

func TestRerunFailedChecksUnsupportedStatusAnd403(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		f := newAPI(t)
		result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, forge.RerunChecksRequest{
			TestedSHA: "sha", TestRef: "openmerge/queue/main/retry", Key: "status",
			Runs: []forge.CheckRun{{ID: 1, Attempt: 1, Name: "legacy", Source: "status:ci", State: forge.Failure}},
		})
		if err != nil || !result.Unsupported {
			t.Fatalf("status rerun = %+v, %v", result, err)
		}
		for path, count := range f.calls {
			if strings.Contains(path, "rerun") && count != 0 {
				t.Fatalf("status rerun made a write: %s=%d", path, count)
			}
		}
	})
	t.Run("actions permission", func(t *testing.T) {
		f := newAPI(t)
		sha, ref := "retry-sha", "openmerge/queue/main/retry"
		id := int64(702)
		f.runs[sha] = []*gh.CheckRun{{ID: &id, Name: new("ci"), HeadSHA: &sha, DetailsURL: new("https://github.test/acme/project/actions/runs/702/job/1"), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: new(int64(15368))}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}}}
		f.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &ref}}
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/runs/702/rerun-failed-jobs") {
				f.apiError(w, http.StatusForbidden, "Resource not accessible by integration")
				return true
			}
			return false
		}
		runs, err := f.driver.CheckRuns(t.Context(), testRepo, sha)
		must(t, err)
		result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, forge.RerunChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: "forbidden"})
		if err != nil || !result.Unsupported {
			t.Fatalf("403 rerun = %+v, %v", result, err)
		}
	})
}

func TestRerunActionsURLAndProducerValidation(t *testing.T) {
	for _, tc := range []struct {
		name, details string
		appID         int64
	}{
		{name: "wrong host", details: "https://evil.test/acme/project/actions/runs/703/job/1", appID: 15368},
		{name: "wrong repository", details: "https://github.test/other/project/actions/runs/704/job/1", appID: 15368},
		{name: "foreign app", details: "https://github.test/acme/project/actions/runs/705/job/1", appID: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			sha, ref := "retry-sha", "openmerge/queue/main/retry"
			id := tc.appID
			checkID := int64(700 + len(tc.name))
			f.runs[sha] = []*gh.CheckRun{{ID: &checkID, Name: new("ci"), HeadSHA: &sha, DetailsURL: &tc.details, Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: &id}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}}}
			f.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &ref}}
			actionsCalls, checkCalls := 0, 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/actions/runs/"):
					actionsCalls++
					w.WriteHeader(http.StatusCreated)
					return true
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rerequest"):
					checkCalls++
					w.WriteHeader(http.StatusCreated)
					return true
				}
				return false
			}
			runs, err := f.driver.CheckRuns(t.Context(), testRepo, sha)
			must(t, err)
			result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, forge.RerunChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: tc.name})
			if err != nil || result.Unsupported || actionsCalls != 0 || checkCalls != 1 {
				t.Fatalf("validated fallback = %+v, %v; actions=%d checks=%d", result, err, actionsCalls, checkCalls)
			}
		})
	}
}

func TestRerunDuplicateNativeIDWithoutAttemptDeduplicates(t *testing.T) {
	f := newAPI(t)
	sha, ref := "retry-sha", "openmerge/queue/main/retry"
	id := int64(99)
	checkID := int64(706)
	f.runs[sha] = []*gh.CheckRun{
		{ID: &checkID, Name: new("ci"), HeadSHA: &sha, DetailsURL: new("https://github.test/acme/project/checks/one"), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: &id}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}},
		{ID: &checkID, Name: new("ci"), HeadSHA: &sha, DetailsURL: new("https://github.test/acme/project/checks/two"), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: &id}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}},
	}
	f.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &ref}}
	checkCalls := 0
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rerequest") {
			checkCalls++
			w.WriteHeader(http.StatusCreated)
			return true
		}
		return false
	}
	runs, err := f.driver.CheckRuns(t.Context(), testRepo, sha)
	must(t, err)
	if len(runs) != 2 || runs[0].Attempt != 1 || runs[1].Attempt != 1 {
		t.Fatalf("duplicate observations invented attempts: %+v", runs)
	}
	result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, forge.RerunChecksRequest{TestedSHA: sha, TestRef: ref, Runs: []forge.CheckRun{runs[0]}, Key: "ambiguous"})
	if err != nil || result.Unsupported || checkCalls != 1 {
		t.Fatalf("duplicate rerun = %+v, %v; check calls=%d", result, err, checkCalls)
	}
}

func TestRerunCheckRequest404IsUnsupported(t *testing.T) {
	f := newAPI(t)
	sha, ref := "retry-sha", "openmerge/queue/main/retry"
	id, checkID := int64(99), int64(707)
	f.runs[sha] = []*gh.CheckRun{{ID: &checkID, Name: new("ci"), HeadSHA: &sha, Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: &id}, CheckSuite: &gh.CheckSuite{ID: new(int64(9))}}}
	f.suites[sha] = []*gh.CheckSuite{{ID: new(int64(9)), Status: new("completed"), HeadBranch: &ref}}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/rerequest") {
			f.apiError(w, http.StatusNotFound, "Not Found")
			return true
		}
		return false
	}
	runs, err := f.driver.CheckRuns(t.Context(), testRepo, sha)
	must(t, err)
	result, err := f.driver.RerunFailedChecks(t.Context(), testRepo, forge.RerunChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: "not-supported"})
	if err != nil || !result.Unsupported {
		t.Fatalf("404 rerun = %+v, %v", result, err)
	}
}
