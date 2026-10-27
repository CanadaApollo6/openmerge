package journal

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoRuntimeProvenanceJournalImports(t *testing.T) {
	err := filepath.WalkDir("../../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if value == "github.com/CanadaApollo6/openmerge/internal/provenance/journal" {
				relative, err := filepath.Rel("../../..", path)
				if err != nil {
					return err
				}
				// Slice 3b permits only the read-only export adapter. Live replay,
				// snapshots and runtime writers remain unwired until 3c/4b.
				if filepath.ToSlash(relative) == "internal/audit/provenance.go" {
					continue
				}
				t.Errorf("runtime provenance journal import in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
