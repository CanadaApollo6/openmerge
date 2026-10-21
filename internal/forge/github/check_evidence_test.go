package github

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	gh "github.com/google/go-github/v93/github"
)

func TestLandingCheckReadContract(t *testing.T) {
	f := newAPI(t)
	sha := "evidence-sha"
	f.runs[sha] = []*gh.CheckRun{
		{ID: new(int64(701)), Name: new("ci"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(int64(15368))}, DetailsURL: new("https://github.test/acme/project/actions/runs/700/attempts/2/job/701")},
		{ID: new(int64(702)), Name: new("ci"), Status: new("completed"), Conclusion: new("failure"), App: &gh.App{ID: new(int64(99))}},
	}
	f.statuses[sha] = []*gh.RepoStatus{{ID: new(int64(703)), Context: new("legacy"), State: new("success"), Creator: &gh.User{Login: new("writer")}}}
	want := []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 2, WorkflowRunID: 700, State: forge.Success, HeadBranch: "openmerge/queue/main/batch"}, {Name: "ci", Source: "app:99", ID: 702, Attempt: 1, AttemptInferred: true, State: forge.Failure, HeadBranch: "openmerge/queue/main/batch"}, {Name: "legacy", Source: "status:writer", ID: 703, State: forge.Success}}
	forgetest.RunCheckEvidence(t, f.driver, testRepo, sha, want)
	// Job enrichment can make an inferred attempt known without inventing a
	// new native check ID; retry-aware landing evidence uses that exact read.
	a := newRerunAPI(t, 700)
	checks, err := a.api.driver.CheckRunsForRetry(t.Context(), testRepo, a.sha, []uint64{700})
	must(t, err)
	if len(checks) != 1 || checks[0].ID != 701 || checks[0].Attempt != 1 || checks[0].AttemptInferred {
		t.Fatal("provider job attempt not distinguished from inference", checks)
	}
}
