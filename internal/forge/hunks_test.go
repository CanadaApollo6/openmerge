package forge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestUnifiedHunkBehaviorGolden(t *testing.T) {
	for _, tc := range []struct {
		name, old, next string
		want            DiffHunk
	}{
		{"replacement", "a\nb\nc\n", "a\nx\nc\n", DiffHunk{1, 3, 1, 3, " a\n-b\n+x\n c\n"}},
		{"insertion", "a\n", "a\nb\n", DiffHunk{1, 1, 1, 2, " a\n+b\n"}},
		{"deletion", "a\nb\n", "a\n", DiffHunk{1, 2, 1, 1, " a\n-b\n"}},
		{"no-newline", "a", "b", DiffHunk{1, 1, 1, 1, "-a\n\\ No newline at end of file\n+b\n\\ No newline at end of file\n"}},
		{"interior-context", "a\nsame\nb\n", "x\nsame\ny\n", DiffHunk{1, 3, 1, 3, "-a\n+x\n same\n-b\n+y\n"}},
		{"crlf", "a\r\nb\r\n", "a\r\nc\r\n", DiffHunk{1, 2, 1, 2, " a\r\n-b\r\n+c\r\n"}},
		{"crlf-to-lf", "a\r\n", "a\n", DiffHunk{1, 1, 1, 1, "-a\r\n+a\n"}},
		{"lf-to-crlf", "a\n", "a\r\n", DiffHunk{1, 1, 1, 1, "-a\n+a\r\n"}},
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
		{"bytes", DiffFile{NewObjectID: "b", BinaryKnown: true}, strings.Repeat("x", MaxDiffHunkInputBytes+1), DiffHunkWorkLimit},
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
	for i := 0; i < MaxDiffFiles; i++ {
		result.Files = append(result.Files, DiffFile{NewPath: fmt.Sprintf("%04d", i), NewObjectID: "b", BinaryKnown: true})
	}
	if err := CollectDiffHunks(t.Context(), &result, map[string]string{"b": "x\n"}); err != nil {
		t.Fatal(err)
	}
	if len(result.Files[MaxDiffFiles-1].Hunks) != 1 || result.Hunks.Completeness != DiffComplete {
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

func TestSeparatedHunkGolden(t *testing.T) {
	for _, n := range []int{300, 70000} {
		r := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
		err := CollectDiffHunks(t.Context(), &r, map[string]string{"a": "a\n" + strings.Repeat("same\n", n) + "b\n", "b": "x\n" + strings.Repeat("same\n", n) + "y\n"})
		want := []DiffHunk{{1, 4, 1, 4, "-a\n+x\n same\n same\n same\n"}, {n - 1, 4, n - 1, 4, " same\n same\n same\n-b\n+y\n"}}
		if err != nil || !reflect.DeepEqual(r.Files[0].Hunks, want) || r.Hunks.Completeness != DiffComplete {
			t.Fatalf("n=%d: %+v %v", n, r, err)
		}
	}
}

func TestUnifiedRetentionBounds(t *testing.T) {
	collect := func(files []DiffFile, blobs map[string]string) RevisionDiff {
		t.Helper()
		r := RevisionDiff{Files: files, Inventory: DiffCoverage{Completeness: DiffComplete}}
		if err := CollectDiffHunks(t.Context(), &r, blobs); err != nil {
			t.Fatal(err)
		}
		return r
	}
	file := DiffFile{NewObjectID: "b", BinaryKnown: true}
	for _, n := range []int{200, 201} {
		r := collect([]DiffFile{file}, map[string]string{"b": strings.Repeat("x\n", n)})
		if n == 200 {
			if r.Hunks.Completeness != DiffComplete {
				t.Fatal(r)
			}
		} else {
			want := DiffHunkRange{0, 0, 1, 201, DiffHunkLineLimit}
			if len(r.Files[0].Hunks) != 0 || !reflect.DeepEqual(r.Files[0].Omitted, []DiffHunkRange{want}) {
				t.Fatal(r)
			}
		}
	}
	r := collect([]DiffFile{file}, map[string]string{"b": strings.Repeat("x", 300001) + "\n"})
	if r.Hunks.Completeness != DiffComplete {
		t.Fatal(r)
	}
	// A large first hunk is omitted whole; the second tiny region still fits.
	a := strings.Repeat("a\n", 201) + strings.Repeat("same\n", 20) + "z\n"
	b := strings.Repeat("b\n", 201) + strings.Repeat("same\n", 20) + "y\n"
	r = collect([]DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}, map[string]string{"a": a, "b": b})
	if len(r.Files[0].Omitted) != 1 || r.Files[0].Omitted[0] != (DiffHunkRange{1, 204, 1, 204, DiffHunkLineLimit}) || len(r.Files[0].Hunks) != 1 || r.Files[0].Hunks[0] != (DiffHunk{219, 4, 219, 4, " same\n same\n same\n-z\n+y\n"}) {
		t.Fatal(r)
	}
	// Byte retention is shared across files, independently of per-file input work.
	r = collect([]DiffFile{{NewPath: "z", NewObjectID: "b", BinaryKnown: true}, {NewPath: "a", NewObjectID: "b", BinaryKnown: true}}, map[string]string{"b": strings.Repeat("x", 1100000) + "\n"})
	if r.Files[0].NewPath != "a" || len(r.Files[0].Hunks) != 1 || r.Files[1].Omitted[0].Reason != DiffHunkByteLimit {
		t.Fatal(r)
	}
}

func TestUnifiedIsolationAndDeadline(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.external")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_CONFIG_KEY_1", "diff.ignoreSubmodules")
	t.Setenv("GIT_CONFIG_VALUE_1", "all")
	h, err := unifiedHunks(t.Context(), "a\n", "b\n")
	if err != nil || !reflect.DeepEqual(h, []DiffHunk{{1, 1, 1, 1, "-a\n+b\n"}}) {
		t.Fatal(h, err)
	}
	// This process has no children; CommandContext kills only our busy shell.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	r := RevisionDiff{Inventory: DiffCoverage{Completeness: DiffComplete}, Files: []DiffFile{{OldObjectID: "a", NewObjectID: "b", BinaryKnown: true}}}
	if err := CollectDiffHunks(t.Context(), &r, map[string]string{"a": "a\n", "b": "b\n"}); err != nil || r.Hunks.Completeness != DiffIncomplete || !slices.Contains(r.Hunks.Reasons, DiffHunkWorkLimit) {
		t.Fatal(r, err)
	}
}
