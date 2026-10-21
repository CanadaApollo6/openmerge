package forgetest

import (
	"reflect"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// RunCheckEvidence verifies a read's stable identity and explicit absence with
// the same assertions for the fake and local GitHub HTTP adapter. No forge call
// in this contract reaches a real service.
func RunCheckEvidence(t *testing.T, driver forge.Driver, repo forge.RepoRef, sha string, want []forge.CheckRun) {
	t.Helper()
	for range 2 {
		got, err := driver.CheckRuns(t.Context(), repo, sha)
		must(t, err)
		strip := func(runs []forge.CheckRun) []forge.CheckRun {
			out := make([]forge.CheckRun, len(runs))
			for i, r := range runs {
				out[i] = forge.CheckRun{ID: r.ID, Attempt: r.Attempt, AttemptInferred: r.AttemptInferred, Name: r.Name, Source: r.Source, State: r.State, WorkflowRunID: r.WorkflowRunID, HeadBranch: r.HeadBranch}
			}
			return out
		}
		if !reflect.DeepEqual(strip(got), strip(want)) {
			t.Fatalf("check identity/result read changed:\n got %+v\nwant %+v", got, want)
		}
	}
	missing, err := driver.CheckRuns(t.Context(), repo, "ffffffffffffffffffffffffffffffffffffffff")
	must(t, err)
	if len(missing) != 0 {
		t.Fatal("absent check fabricated", missing)
	}
}
