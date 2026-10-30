package dependency

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoProductionDependencyImports(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, filepath.Join("..", "..", "internal", "dependency")+string(filepath.Separator)) {
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
			if strings.Contains(filepath.ToSlash(path), "internal/journalreader/") || strings.HasSuffix(filepath.ToSlash(path), "internal/store/shared_reader.go") || strings.HasSuffix(filepath.ToSlash(path), "internal/store/side_append.go") || strings.HasSuffix(filepath.ToSlash(path), "internal/store/dependency_start.go") || strings.HasSuffix(filepath.ToSlash(path), "internal/serve/dependency.go") || strings.HasSuffix(filepath.ToSlash(path), "internal/status/dependencies.go") {
				continue
			}
			if value == "github.com/CanadaApollo6/openmerge/internal/dependency" {
				t.Errorf("runtime dependency import in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
