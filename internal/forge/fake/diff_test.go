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
	if err != nil || result.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(result.Inventory.Reasons, forge.DiffUnpairedRename) {
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

func TestRevisionDiffFakeRawByteLimitCapsFiles(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	files := map[string]string{}
	for i := 0; i < 1400; i++ {
		files[fmt.Sprintf("%04d/", i)+strings.Repeat("segment/", 190)+"file.md"] = "x"
	}
	head := f.repo(repo).commit([]string{base}, nil, "long paths", files)
	got, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: head})
	if err != nil || got.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(got.Inventory.Reasons, forge.DiffByteLimit) || len(got.Files) != forge.MaxDiffFiles {
		t.Fatalf("coverage=%+v files=%d err=%v", got.Inventory, len(got.Files), err)
	}
}

func TestRevisionDiffFakeByteBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			f := New()
			repo := forge.RepoRef{Path: "acme/repo"}
			base, _ := f.Init(repo, "main", "fixture-fence")
			// SHA-256 raw records cost 148 bytes plus the 8-byte path.
			head := f.repo(repo).commit([]string{base}, nil, "boundary", map[string]string{"limit.md": strings.Repeat("x", forge.MaxDiffBytes-156+extra)})
			got, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: base, HeadSHA: head})
			if err != nil || len(got.Files) != 1 {
				t.Fatalf("%+v err=%v", got, err)
			}
			if extra == 0 {
				if got.Inventory.Completeness != forge.DiffComplete || len(got.Inventory.Reasons) != 0 || !got.Files[0].BinaryKnown {
					t.Fatalf("exact boundary: %+v", got)
				}
			} else if got.Inventory.Completeness != forge.DiffIncomplete || !slices.Contains(got.Inventory.Reasons, forge.DiffByteLimit) || got.Files[0].BinaryKnown {
				t.Fatalf("over boundary: %+v", got)
			}
		})
	}
}

func TestRevisionDiffFakeMergeBaseFailures(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	r := f.repo(repo)
	orphan := r.commit(nil, nil, "orphan", nil)
	a := r.commit([]string{base}, nil, "a", nil)
	b := r.commit([]string{base}, nil, "b", nil)
	left := r.commit([]string{a, b}, nil, "left", nil)
	right := r.commit([]string{b, a}, nil, "right", nil)
	for _, tc := range []struct {
		name, base, head string
		reason           forge.DiffFailureReason
	}{
		{"unrelated", base, orphan, forge.DiffNoMergeBase},
		{"multiple", left, right, forge.DiffMultipleMergeBases},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: tc.base, HeadSHA: tc.head})
			if err != nil || got.MergeBaseSHA != "" || got.Inventory.Completeness != forge.DiffIncomplete || len(got.Inventory.Reasons) != 1 || got.Inventory.Reasons[0] != tc.reason {
				t.Fatalf("%+v err=%v", got, err)
			}
		})
	}
}

func TestRevisionDiffFakeUnpairedRenamesIncomplete(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	old := f.repo(repo).commit([]string{base}, nil, "old", map[string]string{"old.md": "same"})
	for _, files := range []map[string]string{{"new.md": "different"}, {"new.md": "same", "copy.md": "same"}} {
		head := f.repo(repo).commit([]string{old}, nil, "uncertain rename", files)
		got, err := f.RevisionDiff(t.Context(), repo, forge.RevisionDiffRequest{BaseSHA: old, HeadSHA: head})
		if err != nil || got.Inventory.Completeness != forge.DiffIncomplete || len(got.Inventory.Reasons) != 1 || got.Inventory.Reasons[0] != forge.DiffUnpairedRename || len(got.Files) != len(files)+1 {
			t.Fatalf("%+v err=%v", got, err)
		}
	}
}
