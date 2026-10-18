package forge

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDiffSemanticsBoundsGolden(t *testing.T) {
	goldens := map[string]string{
		"git-inventory-v2-exact-renames":    "files=1000 bytes=2097152 hunks=0",
		"git-inventory-v3-verified-history": "files=1000 bytes=2097152 hunks=0",
		"git-inventory-v4-bounded-ancestry": "files=1000 bytes=2097152 hunks=0 ancestry=100000 ancestry_duration=30s",
	}
	goldens["git-evidence-v5-single-span"] = "files=1000 bytes=2097152 hunks=512 ancestry=100000 ancestry_duration=30s per_file=1 hunk_bytes=262144 governing_files=256 governing_bytes=1048576"
	goldens["git-evidence-v6-unified-hunks"] = "files=1000 bytes=2097152 ancestry=100000 ancestry_duration=30s lines=200 hunk_bytes=2097152 context=3 input=2097152 output=16781312 file_duration=2s duration=30s governing_files=256 governing_bytes=1048576"
	goldens["git-evidence-v7-bounded-omissions"] = "files=1000 bytes=2097152 ancestry=100000 ancestry_duration=30s lines=200 hunk_bytes=2097152 context=3 input=2097152 output=16781312 file_duration=2s duration=30s governing_files=256 governing_bytes=1048576 omitted_per_file=32 omitted_total=256 summary_per_file=1 stderr=8192 flags=--diff-algorithm=myers --minimal --no-indent-heuristic --inter-hunk-context=0 --unified=3"
	goldens["git-evidence-v8-retained-hunk-budget"] = goldens["git-evidence-v7-bounded-omissions"] + " retained_hunks=4096 escaped_text=4194304"
	want, ok := goldens[DiffSemantics]
	got := fmt.Sprintf("files=%d bytes=%d ancestry=%d ancestry_duration=%s", MaxDiffFiles, MaxDiffBytes, MaxAncestryCommits, MaxAncestryDuration)
	got += fmt.Sprintf(" lines=%d hunk_bytes=%d context=%d input=%d output=%d file_duration=%s duration=%s governing_files=%d governing_bytes=%d", MaxDiffHunkLinesPerFile, MaxDiffHunkBytes, DiffHunkContext, MaxDiffHunkInputBytes, MaxDiffHunkOutputBytes, MaxDiffHunkDuration, MaxDiffHunksDuration, MaxGoverningFiles, MaxGoverningBytes)
	got += fmt.Sprintf(" omitted_per_file=%d omitted_total=%d summary_per_file=1 stderr=%d flags=%s", MaxDiffOmittedRangesPerFile, MaxDiffOmittedRanges, MaxDiffHunkStderrBytes, DiffHunkAlgorithmFlags)
	got += fmt.Sprintf(" retained_hunks=%d escaped_text=%d", MaxDiffHunks, MaxDiffHunkEscapedBytes)
	if !ok || got != want {
		t.Fatalf("bounds changed: %s: %s; bump DiffSemantics and add its golden", DiffSemantics, got)
	}
}

func TestPairExactDiffRenames(t *testing.T) {
	add := func(path, id string) DiffFile {
		return DiffFile{NewPath: path, NewMode: "100644", NewObjectID: id, Rename: DiffNotRenamed}
	}
	del := func(path, id string) DiffFile {
		return DiffFile{OldPath: path, OldMode: "100644", OldObjectID: id, Rename: DiffNotRenamed}
	}
	for _, tc := range []struct {
		name   string
		files  []DiffFile
		count  int
		rename DiffRename
	}{
		{"exact", []DiffFile{del("old", "a"), add("new", "a")}, 1, DiffExactRename},
		{"ambiguous-copies", []DiffFile{del("old", "a"), add("new1", "a"), add("new2", "a")}, 3, DiffRenameUnpaired},
		{"modified-rename", []DiffFile{del("old", "a"), add("new", "b")}, 2, DiffRenameUnpaired},
		{"add-only", []DiffFile{add("new", "a")}, 1, DiffNotRenamed},
		{"delete-only", []DiffFile{del("old", "a")}, 1, DiffNotRenamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PairExactDiffRenames(slices.Clone(tc.files))
			if len(got) != tc.count {
				t.Fatalf("%+v", got)
			}
			for _, file := range got {
				if file.Rename != tc.rename {
					t.Fatalf("%+v", got)
				}
			}
			slices.Reverse(tc.files)
			reversed := PairExactDiffRenames(tc.files)
			if !reflect.DeepEqual(got, reversed) {
				t.Fatalf("ordering changed: %+v %+v", got, reversed)
			}
		})
	}
}

func TestDiffObjectIDs(t *testing.T) {
	for _, id := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		if !ValidDiffObjectID(id) {
			t.Fatal(id)
		}
	}
	for _, id := range []string{"main", "", strings.Repeat("g", 40), strings.Repeat("a", 39), "--help"} {
		if ValidDiffObjectID(id) {
			t.Fatal(id)
		}
	}
	if (DiffCoverage{}).Completeness == DiffComplete {
		t.Fatal("zero value claims completeness")
	}
}
