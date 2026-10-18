package gitops

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestGoverningFilesContract(t *testing.T) {
	f := newFixture(t)
	sha := f.diffCommit(f.base, "governing", map[string]diffEntry{
		".openmerge.yml": {mode: "100644", content: "version: 1\n"}, "CODEOWNERS": {mode: "100644", content: "* @root\n"}, "docs/CODEOWNERS": {mode: "100644", content: "* @ignored\n"}, ".gitattributes": {mode: "100644", content: "* text\n"}, "src/.gitattributes": {mode: "100644", content: "*.go linguist-generated\n"},
	})
	forgetest.RunGoverningFiles(t, f.g, testRepo, sha)
	req := forge.GoverningFilesRequest{CommitSHA: sha, Paths: []string{"src/new.go", "old/deleted.go"}, PathsComplete: true}
	before, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	must(t, err)
	tip := f.diffCommit(sha, "moves", map[string]diffEntry{"CODEOWNERS": {mode: "100644", content: "* @new"}})
	f.push("main", tip, false)
	after, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("moving default changed pinned reads")
	}
}

func TestGoverningFilesModesAndBytes(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name, mode, content string
		reason              forge.DiffFailureReason
	}{
		{"symlink", "120000", "CODEOWNERS", forge.DiffSpecialMode},
		{"size", "100644", strings.Repeat("x", forge.MaxGoverningBytes+1), forge.DiffByteLimit},
		{"exact", "100644", strings.Repeat("x", forge.MaxGoverningBytes), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha := f.diffCommit(f.base, tc.name, map[string]diffEntry{".github/CODEOWNERS": {mode: tc.mode, content: tc.content}, "CODEOWNERS": {mode: "100644", content: "* @fallback"}})
			got, err := f.g.GoverningFiles(t.Context(), testRepo, forge.GoverningFilesRequest{CommitSHA: sha, PathsComplete: true})
			must(t, err)
			if got.SelectedCodeowners != ".github/CODEOWNERS" || len(got.Codeowners) != 1 || !got.Codeowners[0].Present || !forge.ValidDiffObjectID(got.Codeowners[0].ObjectID) {
				t.Fatalf("%+v", got)
			}
			if tc.reason == "" {
				if got.Coverage.Completeness != forge.DiffComplete || len(got.Codeowners[0].Content) != forge.MaxGoverningBytes {
					t.Fatal(got.Coverage)
				}
			} else if !slices.Contains(got.Coverage.Reasons, tc.reason) || got.Codeowners[0].Content != "" {
				t.Fatal(got.Coverage)
			}
		})
	}
}

func TestGoverningFilesMissingBlobAndHistory(t *testing.T) {
	f := newFixture(t)
	sha := f.diffCommit(f.base, "governing", map[string]diffEntry{".openmerge.yml": {mode: "100644", content: "unique missing blob"}})
	req := forge.GoverningFilesRequest{CommitSHA: sha, PathsComplete: true}
	got, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	must(t, err)
	s, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	dir := s.dir
	unlock()
	id := got.Config[0].ObjectID
	must(t, os.Remove(filepath.Join(dir, "objects", id[:2], id[2:])))
	failed, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	if err == nil || !slices.Contains(failed.Coverage.Reasons, forge.DiffReadFailure) {
		t.Fatal(failed.Coverage, err)
	}
	// Even a tree-only read must retain the diff session's shallow protection.
	must(t, os.WriteFile(filepath.Join(dir, "shallow"), []byte(sha+"\n"), 0600))
	failed, err = f.g.GoverningFiles(t.Context(), testRepo, req)
	if err != nil || !slices.Contains(failed.Coverage.Reasons, forge.DiffShallowHistory) {
		t.Fatal(failed.Coverage, err)
	}
}

func TestGoverningLiteralPathsAndHistoryRechecks(t *testing.T) {
	f := newFixture(t)
	sha := f.diffCommit(f.base, "literal attributes", map[string]diffEntry{":(glob)**/.gitattributes": {mode: "100644", content: "* text"}, "weird\tname\n/.gitattributes": {mode: "100644", content: "* -text"}})
	req := forge.GoverningFilesRequest{CommitSHA: sha, Paths: []string{":(glob)**/file", "weird\tname\n/deleted"}, PathsComplete: true}
	got, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	must(t, err)
	present := 0
	for _, file := range got.Attributes {
		if file.Present {
			present++
		}
	}
	if got.Coverage.Completeness != forge.DiffComplete || present != 2 {
		t.Fatalf("literal paths: %+v", got)
	}
	s, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	dir := s.dir
	unlock()
	wrapper := filepath.Join(t.TempDir(), "git")
	// Mutate history after a tree read, before the reader can report completeness.
	writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = ls-tree ]; then\n"+shellQuote(f.g.git)+" \"$@\"\nstatus=$?\nprintf '%s\\n' "+shellQuote(sha)+" > "+shellQuote(filepath.Join(dir, "shallow"))+"\nexit \"$status\"\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
	f.g.git = wrapper
	failed, err := f.g.GoverningFiles(t.Context(), testRepo, req)
	if err != nil || !slices.Contains(failed.Coverage.Reasons, forge.DiffShallowHistory) {
		t.Fatal(failed.Coverage, err)
	}
}
