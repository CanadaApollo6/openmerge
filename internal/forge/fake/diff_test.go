package fake

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestRevisionDiffContract(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	head, err := f.OpenPRWithFiles(repo, 1, base, true, map[string]string{"head.md": "head"})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := f.PushBase(repo, "main", "base moves", map[string]string{"base.go": "base"})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunRevisionDiff(t, f, repo, forge.RevisionDiffRequest{BaseSHA: tip, HeadSHA: head.HeadSHA}, base, "head.md")
}

func TestRevisionDiffFakeEvidenceAndLimits(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	old, err := f.PushBase(repo, "main", "old", map[string]string{"old": "same", "deleted": "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	// Construct a snapshot directly: PR fixture deltas intentionally only add files.
	head := f.repo(repo).commit([]string{old}, nil, "new", map[string]string{"new": "same", "binary": "a\x00b"})
	result, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: old, HeadSHA: head})
	if err != nil || result.Inventory.Completeness != forge.DiffComplete {
		t.Fatalf("%+v %v", result, err)
	}
	renamed, binary, deleted := false, false, false
	for _, file := range result.Files {
		renamed = renamed || file.OldPath == "old" && file.NewPath == "new" && file.Rename == forge.DiffExactRename
		binary = binary || file.NewPath == "binary" && file.Binary && file.BinaryKnown
		deleted = deleted || file.OldPath == "deleted" && file.NewPath == ""
	}
	if !renamed || !binary || !deleted {
		t.Fatalf("lost evidence: %+v", result.Files)
	}
	for _, tc := range []struct {
		name   string
		files  map[string]string
		reason forge.DiffFailureReason
	}{
		{"bytes", map[string]string{"big": strings.Repeat("x", forge.MaxDiffBytes+1)}, forge.DiffByteLimit},
		{"files", func() map[string]string {
			m := map[string]string{}
			for i := 0; i < forge.MaxDiffFiles+1; i++ {
				m[fmt.Sprint(i)] = "x"
			}
			return m
		}(), forge.DiffFileLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha := f.repo(repo).commit([]string{base}, nil, tc.name, tc.files)
			result, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: sha})
			if err != nil || result.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(result.Inventory.Reasons, tc.reason) || len(result.Files) > forge.MaxDiffFiles {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
	missing, _ := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: strings.Repeat("a", 64)})
	if !slices.Contains(missing.Inventory.Reasons, forge.DiffMissingObject) {
		t.Fatalf("%+v", missing)
	}
	f.FailNext("RevisionDiff", errors.New("read failed"))
	failed, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: head})
	if err == nil || !slices.Contains(failed.Inventory.Reasons, forge.DiffReadFailure) {
		t.Fatalf("%+v %v", failed, err)
	}
}
