package fake_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestLandingCheckReadContract(t *testing.T) {
	f, _ := fixture(t)
	runs := []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 2, WorkflowRunID: 700, State: forge.Success, HeadBranch: "openmerge/queue/main/batch"}, {Name: "ci", Source: "app:99", ID: 702, Attempt: 1, AttemptInferred: true, State: forge.Failure, HeadBranch: "openmerge/queue/main/batch"}, {Name: "legacy", Source: "status:writer", ID: 703, State: forge.Success}}
	f.SetCheckRuns(repo, "evidence-sha", runs)
	forgetest.RunCheckEvidence(t, f, repo, "evidence-sha", runs)
}
