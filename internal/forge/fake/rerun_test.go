package fake_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestRerunFailedChecksIsExactAndIdempotent(t *testing.T) {
	repo := forge.RepoRef{Path: "acme/retry"}
	f := fake.NewDeterministic()
	base, _ := f.Init(repo, "main", "openmerge/fence/main")
	pr, err := f.OpenPR(repo, 1, base, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := "openmerge/queue/main/retry"
	tested, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{
		Ref: ref, BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: pr.HeadSHA}}, Method: forge.Merge, BatchID: "retry",
	}, forge.PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f.SetCheckRuns(repo, tested, []forge.CheckRun{{
		ID: 10, Name: "ci", Source: "app:99", State: forge.Failure, HeadBranch: ref,
	}})
	runs, err := f.CheckRuns(t.Context(), repo, tested)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("CheckRuns = %+v", runs)
	}
	first := runs[0]
	request := forge.RerunChecksRequest{
		TestedSHA: tested, TestRef: ref, Runs: []forge.CheckRun{first}, Key: "batch/retry",
	}
	forgetest.RunCheckRerunner(t, repo, f, request, func() ([]forge.CheckRun, error) {
		return f.CheckRuns(t.Context(), repo, tested)
	})
	got, err := f.CheckRuns(t.Context(), repo, tested)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].ID <= got[0].ID || got[1].Attempt <= got[0].Attempt || got[1].State != forge.Pending || got[1].HeadBranch != ref {
		t.Fatalf("rerun snapshot = %+v", got)
	}
}

func retrySetup(t *testing.T, check forge.CheckRun) (*fake.Forge, forge.RepoRef, string, string) {
	t.Helper()
	repo := forge.RepoRef{Path: "acme/retry-states"}
	f := fake.NewDeterministic()
	base, _ := f.Init(repo, "main", "openmerge/fence/main")
	pr, err := f.OpenPR(repo, 1, base, true)
	if err != nil {
		t.Fatal(err)
	}
	ref := "openmerge/queue/main/retry"
	tested, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: ref, BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: pr.HeadSHA}}, Method: forge.Merge, BatchID: "retry"}, forge.PushOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if check.Source == "" {
		check.Source = "app:99"
	}
	if check.HeadBranch == "" {
		check.HeadBranch = ref
	}
	f.SetCheckRuns(repo, tested, []forge.CheckRun{check})
	return f, repo, tested, ref
}

func TestRerunTerminalStatesAndZeroIDAreUnsupportedOrRerunnable(t *testing.T) {
	for _, state := range []forge.CheckState{forge.Cancelled, forge.Skipped, forge.Neutral} {
		t.Run(string(state), func(t *testing.T) {
			f, repo, tested, ref := retrySetup(t, forge.CheckRun{ID: 10, Name: "ci", State: state})
			runs, err := f.CheckRuns(t.Context(), repo, tested)
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.RerunFailedChecks(t.Context(), repo, forge.RerunChecksRequest{TestedSHA: tested, TestRef: ref, Runs: runs, Key: "state"})
			if err != nil || result.Unsupported {
				t.Fatalf("%s rerun = %+v, %v", state, result, err)
			}
			got, err := f.CheckRuns(t.Context(), repo, tested)
			if err != nil || len(got) != 2 || got[1].State != forge.Pending {
				t.Fatalf("%s rerun snapshot = %+v, %v", state, got, err)
			}
		})
	}
	t.Run("zero ID", func(t *testing.T) {
		f, repo, tested, ref := retrySetup(t, forge.CheckRun{Name: "ci", State: forge.Failure})
		runs, err := f.CheckRuns(t.Context(), repo, tested)
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.RerunFailedChecks(t.Context(), repo, forge.RerunChecksRequest{TestedSHA: tested, TestRef: ref, Runs: runs, Key: "zero"})
		if err != nil || !result.Unsupported {
			t.Fatalf("zero ID rerun = %+v, %v", result, err)
		}
	})
}

func TestRerunReplayUsesNewestIdentity(t *testing.T) {
	t.Run("higher ID", func(t *testing.T) {
		f, repo, tested, ref := retrySetup(t, forge.CheckRun{ID: 10, Attempt: 1, Name: "ci", State: forge.Failure})
		request := forge.RerunChecksRequest{TestedSHA: tested, TestRef: ref, Runs: []forge.CheckRun{{ID: 10, Attempt: 1, Name: "ci", Source: "app:99", State: forge.Failure, HeadBranch: ref}}, Key: "first"}
		result, err := f.RerunFailedChecks(t.Context(), repo, request)
		if err != nil || result.Unsupported {
			t.Fatalf("initial rerun = %+v, %v", result, err)
		}
		request.Key = "after-cache-loss"
		result, err = f.RerunFailedChecks(t.Context(), repo, request)
		if err != nil || result.Unsupported {
			t.Fatalf("replay = %+v, %v", result, err)
		}
		got, err := f.CheckRuns(t.Context(), repo, tested)
		if err != nil || len(got) != 2 {
			t.Fatalf("replay dispatched a duplicate: %+v, %v", got, err)
		}
	})
	t.Run("higher attempt same ID", func(t *testing.T) {
		f, repo, tested, ref := retrySetup(t, forge.CheckRun{ID: 10, Attempt: 1, Name: "ci", State: forge.Failure})
		f.SetCheckRuns(repo, tested, []forge.CheckRun{
			{ID: 10, Attempt: 1, Name: "ci", Source: "app:99", State: forge.Failure, HeadBranch: ref},
			{ID: 10, Attempt: 2, Name: "ci", Source: "app:99", State: forge.Pending, HeadBranch: ref},
		})
		result, err := f.RerunFailedChecks(t.Context(), repo, forge.RerunChecksRequest{TestedSHA: tested, TestRef: ref, Runs: []forge.CheckRun{{ID: 10, Attempt: 1, Name: "ci", Source: "app:99", State: forge.Failure, HeadBranch: ref}}, Key: "same-id"})
		if err != nil || result.Unsupported {
			t.Fatalf("same-ID replay = %+v, %v", result, err)
		}
		got, err := f.CheckRuns(t.Context(), repo, tested)
		if err != nil || len(got) != 2 {
			t.Fatalf("same-ID replay dispatched a duplicate: %+v, %v", got, err)
		}
	})
}
