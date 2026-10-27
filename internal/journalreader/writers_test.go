package journalreader

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The new sealed interfaces cannot enter the queue.Event append APIs. Enforce
// common escape hatches: production codecs, snapshot calls and raw literals.
// This is a tripwire, not proof: concatenation, .sql and _test.go are not covered.
func TestNoProductionNewEventOrSnapshotWriters(t *testing.T) {
	must(t, filepath.WalkDir("../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]string{}
		for _, spec := range file.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if value != "github.com/CanadaApollo6/openmerge/internal/dependency" && value != "github.com/CanadaApollo6/openmerge/internal/provenance/journal" {
				continue
			}
			alias := filepath.Base(value)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias == "." || alias == "_" {
				t.Errorf("opaque evidence codec import in %s", path)
			}
			aliases[alias] = value
			slash := filepath.ToSlash(path)
			if !strings.Contains(slash, "internal/journalreader/") && !strings.HasSuffix(slash, "internal/store/shared_reader.go") && !strings.HasSuffix(slash, "internal/store/side_append.go") && !strings.Contains(slash, "internal/dependency/") {
				t.Errorf("new event codec escaped read-only boundary: %s", path)
			}
		}
		allowedKinds := map[string]bool{
			"internal/journalreader/reader.go":      true,
			"internal/provenance/journal/events.go": true,
			"internal/provenance/journal/codec.go":  true,
			"internal/dependency/codec.go":          true,
			"internal/dependency/projection.go":     true,
			"internal/dependency/snapshot.go":       true,
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		ast.Inspect(file, func(n ast.Node) bool {
			// Include function-value captures, not just direct calls.
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "AppendSide" && rel != "internal/store/side_append.go" {
				t.Errorf("production side append caller in %s", path)
			}

			if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING && !allowedKinds[rel] {
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				for _, kind := range []string{"change_provenance_observed", "landing_provenance_recorded", "dependency_journal_initialized", "change_dependencies_declared", "dependency_policy_applied", "dependency_declaration_acknowledged", "dependency_declaration_self_acknowledged", "dependency_observed"} {
					if strings.Contains(value, kind) {
						t.Errorf("new kind literal outside read-only codec allowlist in %s: %s", path, kind)
					}
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "EncodeSnapshot" && strings.Contains(filepath.ToSlash(path), "internal/dependency/") {
				t.Errorf("production unqualified v8 snapshot encoding in %s", path)
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || aliases[recv.Name] == "" {
				return true
			}
			if sel.Sel.Name == "EncodeSnapshot" && rel != "internal/journalreader/snapshot.go" {
				t.Errorf("production v8/v9 snapshot writer in %s", path)
			}
			// Only pure contract folds may call new-event encoders. Reader code decodes;
			// any later persistence adapter must be separately reviewed with this fence.
			if sel.Sel.Name == "EncodeEvent" && !strings.Contains(filepath.ToSlash(path), "internal/dependency/") && rel != "internal/store/side_append.go" {
				t.Errorf("production new-event encoding outside pure fold in %s", path)
			}
			return true
		})
		return nil
	}))
}
