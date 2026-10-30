package serve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// Keep pool lifetimes reviewable: all bubbles use testSynctest, and functions
// entering a bubble must create their database fixtures inside its callback.
// Follow local helpers too, so wrapping fixture in another factory cannot hide
// an outer pool. This guard runs without PostgreSQL.
func TestSynctestFixtureOwnership(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*_test.go")
	must(t, err)
	funcs := map[string]*ast.FuncDecl{}
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		must(t, err)
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			must(t, err)
			if path == "testing/synctest" && fset.Position(file.Pos()).Filename != "synctest_test.go" {
				// Wait is fine elsewhere; Test must go through the shared helper.
				name := "synctest"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				ast.Inspect(file, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == name && sel.Sel.Name == "Test" {
							t.Errorf("%s: use testSynctest", fset.Position(sel.Pos()))
						}
					}
					return true
				})
				if name == "." {
					t.Errorf("%s: do not dot-import testing/synctest", fset.Position(imp.Pos()))
				}
			}
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	// databaseConnection is the shared pool constructor. Find every transitive
	// fixture factory rather than maintaining a list of fixture names.
	factories := map[string]bool{"databaseConnection": true}
	for changed := true; changed; {
		changed = false
		for name, fn := range funcs {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && factories[id.Name] && !factories[name] {
						factories[name], changed = true, true
					}
				}
				return true
			})
		}
	}
	for _, fn := range funcs {
		entersBubble := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "testSynctest" {
					entersBubble = true
				}
			}
			return true
		})
		if !entersBubble {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name == "testSynctest" {
				return false
			}
			if factories[id.Name] {
				t.Errorf("%s: create %s inside testSynctest using the inner t", fset.Position(call.Pos()), id.Name)
			}
			return true
		})
	}
}
