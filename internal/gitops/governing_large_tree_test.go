package gitops

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestGoverningLargeTreeDoesNotPause(t *testing.T) {
	f := newFixture(t)
	files := map[string]diffEntry{
		".gitattributes":                 {mode: "100644", content: "* text"},
		"unobserved/deep/.gitattributes": {mode: "100644", content: "*.go linguist-generated"},
		"empty/.gitattributes":           {mode: "100644", content: ""},
	}
	sourceNamesBytes := 0
	sourceFiles := 50_001
	sourceDir := "monorepo/large-source-directory-with-padding/"
	if testing.Short() {
		// Long shared directory names cross the same byte boundary with
		// fewer index entries; no source files need to exist on disk.
		sourceFiles = 1_024
		sourceDir += strings.Repeat(strings.Repeat("p", 240)+"/", 9)
	}
	for i := range sourceFiles {
		name := fmt.Sprintf("%ssource-file-%06d.go", sourceDir, i)
		sourceNamesBytes += len(name) + 1
		files[name] = diffEntry{mode: "100644", content: "package fixture\n"}
	}
	if sourceNamesBytes <= forge.MaxDiffBytes {
		t.Fatal("fixture does not exceed the old recursive-tree output budget")
	}
	t.Logf("%d source files contribute %d listing bytes (budget %d)", sourceFiles, sourceNamesBytes, forge.MaxDiffBytes)
	sha := f.diffCommit(f.base, "large monorepo governing tree", files)
	all, err := f.g.GoverningFiles(t.Context(), testRepo, forge.GoverningFilesRequest{CommitSHA: sha, AllAttributes: true})
	must(t, err)
	if all.Coverage.Completeness != forge.DiffComplete {
		t.Fatalf("unrelated source files paused governing policy: %+v", all.Coverage)
	}
	present := map[string]bool{}
	for _, file := range all.Attributes {
		if file.Present {
			present[file.Path] = true
		}
	}
	for _, name := range []string{".gitattributes", "unobserved/deep/.gitattributes", "empty/.gitattributes"} {
		if !present[name] {
			t.Fatalf("targeted inventory lost governing file %s", name)
		}
	}
	if len(present) != 3 {
		t.Fatalf("unrelated files entered governing inventory: %v", present)
	}
	targeted, err := f.g.GoverningFiles(t.Context(), testRepo, forge.GoverningFilesRequest{CommitSHA: sha, Paths: []string{sourceDir + "source-file-000000.go"}, PathsComplete: true})
	must(t, err)
	if targeted.Coverage.Completeness != forge.DiffComplete {
		t.Fatalf("changed-path probes paused on large tree: %+v", targeted.Coverage)
	}
}

func TestGoverningTargetedInventoryRejectsAttributeDirectory(t *testing.T) {
	f := newFixture(t)
	sha := f.diffCommit(f.base, "attribute path is directory", map[string]diffEntry{"nested/.gitattributes/file": {mode: "100644", content: "not attributes"}})
	got, err := f.g.GoverningFiles(t.Context(), testRepo, forge.GoverningFilesRequest{CommitSHA: sha, AllAttributes: true})
	must(t, err)
	if got.Coverage.Completeness == forge.DiffComplete {
		t.Fatal("directory masquerading as attributes was accepted")
	}
}
