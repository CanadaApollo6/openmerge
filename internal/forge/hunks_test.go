package forge

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestHunkContentAndRanges(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		want            DiffHunk
	}{
		{"replacement", "a\nb\nc\n", "a\nx\nc\n", DiffHunk{2, 1, 2, 1, "-b\n+x\n"}},
		{"insertion", "a\n", "a\nb\n", DiffHunk{1, 0, 2, 1, "+b\n"}},
		{"deletion", "a\nb\n", "a\n", DiffHunk{2, 1, 1, 0, "-b\n"}},
		{"no-newline", "a", "b", DiffHunk{1, 1, 1, 1, "-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"}},
		{"coarse-span", "a\nsame\nb\n", "x\nsame\ny\n", DiffHunk{1, 3, 1, 3, "-a\n-same\n-b\n+x\n+same\n+y\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
			if err := CollectDiffHunks(t.Context(), &result, map[string]string{"a": tc.old, "b": tc.next}); err != nil {
				t.Fatal(err)
			}
			f := result.Files[0]
			if result.Hunks.Completeness != DiffComplete || f.HunkCoverage.Completeness != DiffComplete || len(f.Hunks) != 1 || f.Hunks[0] != tc.want {
				t.Fatalf("%+v %+v", result, f)
			}
		})
	}
}

func TestHunkLimitsAndSpecialFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   DiffFile
		text   string
		reason DiffFailureReason
	}{
		{"binary", DiffFile{Binary: true, BinaryKnown: true}, "", DiffNonText},
		{"unknown-binary", DiffFile{}, "", DiffNonText},
		{"symlink", DiffFile{NewMode: "120000", BinaryKnown: true}, "target", DiffSpecialMode},
		{"submodule", DiffFile{Submodule: true}, "", DiffSpecialMode},
		{"mode", DiffFile{OldMode: "100644", NewMode: "100755", BinaryKnown: true}, "", DiffSpecialMode},
		{"encoding", DiffFile{NewObjectID: "b", BinaryKnown: true}, "\xff", DiffInvalidUTF8},
		{"bytes", DiffFile{NewObjectID: "b", BinaryKnown: true}, strings.Repeat("x", MaxDiffHunkBytes), DiffHunkByteLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{tc.file}}
			if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": tc.text}); err != nil {
				t.Fatal(err)
			}
			f := result.Files[0]
			if result.Hunks.Completeness != DiffIncomplete || !slices.Contains(result.Hunks.Reasons, tc.reason) || len(f.Hunks) != 0 || len(f.Omitted) != 1 || f.Omitted[0].Reason != tc.reason {
				t.Fatalf("%+v %+v", result, f)
			}
		})
	}
	result := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}}
	for i := 0; i < MaxDiffHunks+1; i++ {
		result.Files = append(result.Files, DiffFile{NewPath: fmt.Sprint(i), NewObjectID: "b", BinaryKnown: true})
	}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": "x\n"}); err != nil {
		t.Fatal(err)
	}
	if len(result.Files[MaxDiffHunks-1].Hunks) != 1 || result.Files[MaxDiffHunks].Omitted[0].Reason != DiffHunkLimit {
		t.Fatal(result.Hunks)
	}
	// Exact byte budget includes the line marker, newline and all content.
	result.Files = []DiffFile{{NewObjectID: "b", BinaryKnown: true}}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": strings.Repeat("x", MaxDiffHunkBytes-2) + "\n"}); err != nil {
		t.Fatal(err)
	}
	if result.Hunks.Completeness != DiffComplete || len(result.Files[0].Hunks[0].Text) != MaxDiffHunkBytes {
		t.Fatal(result.Hunks)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CollectDiffHunks(ctx, &result, nil); err == nil || !slices.Contains(result.Hunks.Reasons, DiffReadFailure) {
		t.Fatal(result.Hunks, err)
	}
}
