package forge

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

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
