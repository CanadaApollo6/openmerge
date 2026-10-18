package forgetest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// RunRevisionDiff exercises both fake and git-backed readers over a fixture
// whose pinned endpoints have diverged from MergeBase, with one head-only file.
func RunRevisionDiff(t *testing.T, reader forge.RevisionDiffReader, repo forge.RepoRef, req forge.RevisionDiffRequest, mergeBase, path string) {
	t.Helper()
	result, err := reader.RevisionDiff(t.Context(), repo, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseSHA != req.BaseSHA || result.HeadSHA != req.HeadSHA || result.MergeBaseSHA != mergeBase || result.Semantics != forge.DiffSemantics || result.Inventory.Completeness != forge.DiffComplete || len(result.Inventory.Reasons) != 0 {
		t.Fatalf("unpinned or incomplete diff: %+v", result)
	}
	if len(result.Files) != 1 || result.Files[0].NewPath != path || result.Files[0].OldPath != "" || result.Files[0].NewMode != "100644" || !forge.ValidDiffObjectID(result.Files[0].NewObjectID) || !result.Files[0].BinaryKnown {
		t.Fatalf("inventory = %+v", result.Files)
	}
	if result.Hunks.Completeness != forge.DiffIncomplete || len(result.Hunks.Reasons) != 1 || result.Hunks.Reasons[0] != forge.DiffHunksNotCollected || len(result.Files[0].Hunks) != forge.MaxDiffHunks {
		t.Fatalf("2a must not claim hunk coverage: %+v", result)
	}
	// Evidence returned to a consumer must not alias an internal cache/fixture.
	original, err := reader.RevisionDiff(t.Context(), repo, req)
	if err != nil {
		t.Fatal(err)
	}
	result.Files[0].NewPath = "mutated"
	result.Hunks.Reasons[0] = forge.DiffReadFailure
	repeated, err := reader.RevisionDiff(t.Context(), repo, req)
	if err != nil || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("read was mutable: %+v %v", repeated, err)
	}
	empty, err := reader.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: req.HeadSHA, HeadSHA: req.HeadSHA})
	if err != nil || empty.Inventory.Completeness != forge.DiffComplete || len(empty.Files) != 0 || empty.MergeBaseSHA != req.HeadSHA {
		t.Fatalf("empty diff: %+v %v", empty, err)
	}
	for _, tc := range []struct {
		name, base string
		reason     forge.DiffFailureReason
	}{
		{"mutable ref", "main", forge.DiffInvalidObjectID},
		{"missing commit", strings.Repeat("a", len(req.BaseSHA)), forge.DiffMissingObject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := reader.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: tc.base, HeadSHA: req.HeadSHA})
			if err == nil || result.Inventory.Completeness != forge.DiffIncomplete || len(result.Inventory.Reasons) != 1 || result.Inventory.Reasons[0] != tc.reason {
				t.Fatalf("failure contract: %+v err=%v", result, err)
			}
		})
	}
}
