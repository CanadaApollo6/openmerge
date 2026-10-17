package fake_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCancelChecksIsExactAndIdempotent(t *testing.T) {
	f, repo, sha, ref := retrySetup(t, forge.CheckRun{ID: 10, Attempt: 2, Name: "ci", State: forge.Pending})
	runs, err := f.CheckRuns(t.Context(), repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	other := runs[0]
	other.ID = 11
	f.SetCheckRuns(repo, sha, append(runs, other))
	req := forge.CancelChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: "batch/cancel"}
	for range 2 {
		result, err := f.CancelChecks(t.Context(), repo, req)
		if err != nil || result.Unsupported != "" || len(result.Confirmed) != 1 || result.Confirmed[0].ID != 10 || result.Confirmed[0].State != forge.Cancelled {
			t.Fatalf("cancellation: %+v %v", result, err)
		}
	}
	current, err := f.CheckRuns(t.Context(), repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	if current[1].State != forge.Pending {
		t.Fatalf("unidentified run cancelled: %+v", current)
	}
}

func TestCancelChecksValidatesWholeRequestWithoutCancellingNewAttempt(t *testing.T) {
	for _, variation := range []string{"sha", "ref", "id", "attempt", "source", "name", "branch", "workflow"} {
		t.Run(variation, func(t *testing.T) {
			f, repo, sha, ref := retrySetup(t, forge.CheckRun{ID: 10, Attempt: 2, Name: "ci", State: forge.Pending})
			runs, err := f.CheckRuns(t.Context(), repo, sha)
			if err != nil {
				t.Fatal(err)
			}
			req := forge.CancelChecksRequest{TestedSHA: sha, TestRef: ref, Runs: append(runs, runs[0]), Key: "batch/cancel"}
			switch variation {
			case "sha":
				req.TestedSHA = "other"
			case "ref":
				req.TestRef = "other"
			case "id":
				req.Runs[1].ID = 11
			case "attempt":
				req.Runs[1].Attempt = 1
			case "source":
				req.Runs[1].Source = "status:ci"
			case "name":
				req.Runs[1].Name = "other"
			case "branch":
				req.Runs[1].HeadBranch = "main"
			case "workflow":
				req.Runs[1].WorkflowRunID = 99
			}
			result, err := f.CancelChecks(t.Context(), repo, req)
			if err != nil || result.Unsupported == "" || len(result.Confirmed) != 0 {
				t.Fatalf("request: %+v %v", result, err)
			}
			current, err := f.CheckRuns(t.Context(), repo, sha)
			if err != nil {
				t.Fatal(err)
			}
			if current[0].State != forge.Pending {
				t.Fatalf("partial malformed request cancelled run: %+v", current)
			}
		})
	}
}

func TestCancelCompletedChecksConfirmsExistingOutcome(t *testing.T) {
	for _, state := range []forge.CheckState{forge.Success, forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral} {
		t.Run(string(state), func(t *testing.T) {
			f, repo, sha, ref := retrySetup(t, forge.CheckRun{ID: 10, Name: "ci", State: state})
			runs, err := f.CheckRuns(t.Context(), repo, sha)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.CancelChecks(t.Context(), repo, forge.CancelChecksRequest{TestedSHA: sha, TestRef: ref, Runs: runs, Key: "cancel"})
			if err != nil || result.Unsupported != "" || len(result.Confirmed) != 1 || result.Confirmed[0].State != state {
				t.Fatalf("completed: %+v %v", result, err)
			}
		})
	}
}
