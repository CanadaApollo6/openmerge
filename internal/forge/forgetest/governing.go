package forgetest

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// RunGoverningFiles expects a root config, root CODEOWNERS, root attributes,
// and src/.gitattributes; .github/CODEOWNERS must be absent. Paths exercise
// both rename sides, a deletion, and deduplication of ancestor probes.
func RunGoverningFiles(t *testing.T, reader forge.GoverningFilesReader, repo forge.RepoRef, sha string) {
	t.Helper()
	req := forge.GoverningFilesRequest{CommitSHA: sha, Paths: []string{"src/nested/new.go", "old/deleted.go", "src/old.go"}, PathsComplete: true}
	got, err := reader.GoverningFiles(t.Context(), repo, req)
	if err != nil || got.CommitSHA != sha || got.Semantics != forge.DiffSemantics || got.Coverage.Completeness != forge.DiffComplete || len(got.Coverage.Reasons) != 0 {
		t.Fatalf("governing basis: %+v %v", got, err)
	}
	if len(got.Config) != 4 || len(got.Codeowners) != 2 || got.SelectedCodeowners != "CODEOWNERS" || len(got.Attributes) != 4 {
		t.Fatalf("probes: %+v", got)
	}
	probePaths := func(files []forge.GoverningFile) []string {
		paths := []string{}
		for _, file := range files {
			paths = append(paths, file.Path)
		}
		return paths
	}
	if !slices.Equal(probePaths(got.Config), []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml"}) || !slices.Equal(probePaths(got.Codeowners), []string{".github/CODEOWNERS", "CODEOWNERS"}) || !slices.Equal(probePaths(got.Attributes), []string{".gitattributes", "old/.gitattributes", "src/.gitattributes", "src/nested/.gitattributes"}) {
		t.Fatalf("selection/order semantics drift: %+v", got)
	}
	paths := []string{}
	for _, group := range [][]forge.GoverningFile{got.Config, got.Codeowners, got.Attributes} {
		for _, file := range group {
			paths = append(paths, file.Path)
			present := slices.Contains([]string{".openmerge.yml", "CODEOWNERS", ".gitattributes", "src/.gitattributes"}, file.Path)
			if file.Coverage.Completeness != forge.DiffComplete || file.Present != present {
				t.Fatalf("presence: %+v", file)
			}
			if present {
				if !forge.ValidDiffObjectID(file.ObjectID) || file.Mode != "100644" || file.Content == "" {
					t.Fatalf("provenance: %+v", file)
				}
			} else if file.ObjectID != "" || file.Content != "" || file.Mode != "" {
				t.Fatalf("false absence: %+v", file)
			}
		}
	}
	if !slices.Contains(paths, "old/.gitattributes") || !slices.Contains(paths, "src/nested/.gitattributes") {
		t.Fatal(paths)
	}
	original, err := reader.GoverningFiles(t.Context(), repo, req)
	if err != nil {
		t.Fatal(err)
	}
	got.Config[0].Content = "mutated"
	got.Attributes[0].Path = "mutated"
	repeated, err := reader.GoverningFiles(t.Context(), repo, req)
	if err != nil || !reflect.DeepEqual(original, repeated) {
		t.Fatalf("mutable evidence: %+v %v", repeated, err)
	}
	req.PathsComplete = false
	incomplete, err := reader.GoverningFiles(t.Context(), repo, req)
	if err != nil || !slices.Contains(incomplete.Coverage.Reasons, forge.DiffInventoryIncomplete) {
		t.Fatalf("partial paths: %+v %v", incomplete, err)
	}
	for _, tc := range []struct {
		pin    string
		reason forge.DiffFailureReason
	}{{"main", forge.DiffInvalidObjectID}, {strings.Repeat("a", len(sha)), forge.DiffMissingObject}} {
		req.CommitSHA = tc.pin
		failed, err := reader.GoverningFiles(t.Context(), repo, req)
		if err == nil || !slices.Contains(failed.Coverage.Reasons, tc.reason) {
			t.Fatalf("failure: %+v %v", failed, err)
		}
	}
	req.CommitSHA = sha
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failed, err := reader.GoverningFiles(ctx, repo, req)
	if err == nil || !slices.Contains(failed.Coverage.Reasons, forge.DiffReadFailure) {
		t.Fatalf("cancellation: %+v %v", failed, err)
	}
}

// RunGoverningLimitations expects an invalid UTF-8 config, an implicit
// .github/CODEOWNERS directory, a root fallback, and root attributes.
func RunGoverningLimitations(t *testing.T, reader forge.GoverningFilesReader, repo forge.RepoRef, sha string) {
	t.Helper()
	got, err := reader.GoverningFiles(t.Context(), repo, forge.GoverningFilesRequest{CommitSHA: sha, PathsComplete: true})
	if err != nil || got.Coverage.Completeness != forge.DiffIncomplete || !slices.Contains(got.Coverage.Reasons, forge.DiffInvalidUTF8) || !slices.Contains(got.Coverage.Reasons, forge.DiffSpecialMode) {
		t.Fatalf("%+v %v", got, err)
	}
	if len(got.Config) != 4 || got.Config[0].Content != "" || !slices.Contains(got.Config[0].Coverage.Reasons, forge.DiffInvalidUTF8) {
		t.Fatal(got)
	}
	if got.SelectedCodeowners != ".github/CODEOWNERS" || len(got.Codeowners) != 1 || !got.Codeowners[0].Present || got.Codeowners[0].Mode != "040000" || got.Codeowners[0].Content != "" {
		t.Fatal(got)
	}
	if len(got.Attributes) != 1 || !got.Attributes[0].Present || got.Attributes[0].Content != "* text\n" {
		t.Fatal(got)
	}
}
