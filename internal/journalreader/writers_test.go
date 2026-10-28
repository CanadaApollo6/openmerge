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
		rel := strings.TrimPrefix(filepath.ToSlash(path), "../../")
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
			auditDecoder := rel == "internal/audit/provenance.go" && value == "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
			if !strings.Contains(slash, "internal/journalreader/") && !strings.HasSuffix(slash, "internal/store/shared_reader.go") && !strings.HasSuffix(slash, "internal/store/side_append.go") && !strings.Contains(slash, "internal/dependency/") && !auditDecoder && !dependencySeam(rel, value) {
				t.Errorf("new event codec escaped read-only boundary: %s", path)
			}
		}
		// Audit may name and decode provenance rows; encoder calls remain forbidden.
		allowedKinds := map[string]bool{
			"internal/journalreader/reader.go":      true,
			"internal/provenance/journal/events.go": true,
			"internal/provenance/journal/codec.go":  true,
			"internal/dependency/codec.go":          true,
			"internal/dependency/projection.go":     true,
			"internal/dependency/snapshot.go":       true,
			"internal/dependency/decision.go":       true,
		}
		allowedStarts := disabledCompanionStarts(file, rel)
		activated := activatedProvenanceCalls(file, rel)
		dependencyCalls := activatedCalls(file, rel, "dependencies")
		transactions := sanctionedLandingTransactions(file, rel)
		ast.Inspect(file, func(n ast.Node) bool {
			// Include function-value captures, not just direct calls.
			if sel, ok := n.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "AppendSide":
					if rel != "internal/store/side_append.go" {
						t.Errorf("production side append caller in %s", path)
					}
				case "RecoverLanded":
					if rel != "internal/store/side_append.go" {
						t.Errorf("production landed recovery caller in %s", path)
					}
				case "RecoverProvenance", "StartProvenanceLanding":
					if !activated[sel] {
						t.Errorf("ungated provenance writer caller/capture in %s", path)
					}
				case "StartDependencyLanding", "WithDependencyActivation":
					if !dependencyCalls[sel] {
						t.Errorf("ungated dependency writer caller/capture in %s", path)
					}
				case "appendDependencyCompositionTx":
 if rel!="internal/store/store.go" { t.Errorf("composition admission writer outside queue transaction in %s",path) }
 case "startID", "buildStartID":
					if rel != "internal/store/dependency_start.go" && rel != "internal/store/side_append.go" {
						t.Errorf("dependency companion admission outside its start transaction in %s", path)
					}
				case "startLandingTransaction":
					if !transactions[sel] {
						t.Errorf("production transaction start caller/capture in %s", path)
					}
				case "appendSideTx":
					if rel != "internal/store/side_append.go" && rel != "internal/store/landing_start.go" {
						t.Errorf("production transaction append caller in %s", path)
					}
				case "startLandingWithSide":
					if !allowedStarts[sel] {
						t.Errorf("production companion start caller/capture in %s", path)
					}
				}
			}

			if kv, ok := n.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "startID" || key.Name == "buildStartID") && rel != "internal/store/dependency_start.go" {
					t.Errorf("dependency companion admission outside its start transaction in %s", path)
				}
			}
			if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING && !allowedKinds[rel] {
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				for _, kind := range []string{"change_provenance_observed", "landing_provenance_recorded", "dependency_journal_initialized", "change_dependencies_declared", "dependency_policy_applied", "dependency_declaration_acknowledged", "dependency_declaration_self_acknowledged", "dependency_observed", "dependency_decision_recorded", "dependency_composition_recorded", "dependency_compositions_invalidated"} {
					if rel == "internal/audit/provenance.go" && (kind == "change_provenance_observed" || kind == "landing_provenance_recorded") {
						continue
					}
					if rel=="internal/store/authority_downgrade.go" && (kind=="dependency_composition_recorded" || kind=="dependency_compositions_invalidated") { continue }
 if rel=="internal/queue/wire.go" && kind=="dependency_compositions_invalidated" { continue }
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

// Only the ordinary landing wrapper may reference the seam, as a direct call
// with a literal nil builder. A function-value capture would bypass argument
// inspection, so it is deliberately absent from this set.
func disabledCompanionStarts(file *ast.File, rel string) map[*ast.SelectorExpr]bool {
	allowed := map[*ast.SelectorExpr]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || rel != "internal/store/landing_start.go" || len(call.Args) < 9 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "startLandingWithSide" {
			return true
		}
		if arg, ok := call.Args[8].(*ast.Ident); ok && arg.Name == "nil" {
			allowed[sel] = true
		}
		return true
	})
	return allowed
}

func TestCompanionStartHoldGuard(t *testing.T) {
	for _, tc := range []struct {
		source, path string
		want         bool
	}{
		{`s.startLandingWithSide(a,b,c,d,e,f,g,h,nil)`, "internal/store/landing_start.go", true},
		{`s.startLandingWithSide(a,b,c,d,e,f,g,h,build)`, "internal/store/landing_start.go", false},
		{`s.startLandingWithSide(a,b,c,d,e,f,g,h,nil)`, "internal/serve/worker.go", false},
		{`_ = s.startLandingWithSide`, "internal/store/landing_start.go", false},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "guard.go", "package store; func guard(){"+tc.source+"}", 0)
		must(t, err)
		allowed := disabledCompanionStarts(file, tc.path)
		if (len(allowed) == 1) != tc.want {
			t.Fatalf("guard mismatch for %s in %s", tc.source, tc.path)
		}
	}
}

// Only direct calls inside the private queue activation branch may enter the
// sanctioned writer or startup recovery. Function captures remain forbidden.
func activatedProvenanceCalls(file *ast.File, rel string) map[*ast.SelectorExpr]bool {
	return activatedCalls(file, rel, "provenance")
}

// dependencySeam names the only production files outside the read-only boundary
// allowed to import the dependency package: the store start adapter and the
// private serve activation seam. Neither may encode new events.
func dependencySeam(rel, value string) bool {
	return value == "github.com/CanadaApollo6/openmerge/internal/dependency" && (rel == "internal/store/dependency_start.go" || rel == "internal/serve/dependency.go")
}

// activatedCalls returns direct calls inside an `if x.<field> != nil` branch of
// the private serve activation files. Function captures remain forbidden.
func activatedCalls(file *ast.File, rel, field string) map[*ast.SelectorExpr]bool {
	out := map[*ast.SelectorExpr]bool{}
	if rel != "internal/serve/serve.go" && rel != "internal/serve/worker.go" && rel != "internal/serve/stage_a_gate.go" {
		return out
	}
	ast.Inspect(file, func(n ast.Node) bool {
		branch, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		condition, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || condition.Op != token.NEQ {
			return true
		}
		sel, ok := condition.X.(*ast.SelectorExpr)
		nilValue, nilOK := condition.Y.(*ast.Ident)
		if !ok || sel.Sel.Name != field || !nilOK || nilValue.Name != "nil" {
			return true
		}
		ast.Inspect(branch.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					out[sel] = true
				}
			}
			return true
		})
		return true
	})
	return out
}

func TestActivatedProvenanceWriterGuard(t *testing.T) {
	for _, tc := range []struct {
		source, path string
		want         bool
	}{
		{`if w.provenance != nil { s.store.RecoverProvenance(ctx,w.id) }`, "internal/serve/serve.go", true},
		{`if w.provenance != nil { s.store.StartProvenanceLanding() }`, "internal/serve/stage_a_gate.go", true},
		{`s.store.RecoverProvenance(ctx,w.id)`, "internal/serve/serve.go", false},
		{`if w.provenance == nil { s.store.RecoverProvenance(ctx,w.id) }`, "internal/serve/serve.go", false},
		{`if w.provenance != nil { _ = s.store.RecoverProvenance }`, "internal/serve/worker.go", false},
		{`if w.provenance != nil { s.store.StartProvenanceLanding() }`, "cmd/openmerge/main.go", false},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "guard.go", "package serve; func f(){"+tc.source+"}", 0)
		must(t, err)
		if (len(activatedProvenanceCalls(file, tc.path)) > 0) != tc.want {
			t.Fatal(tc.source)
		}
	}
}

func sanctionedLandingTransactions(file *ast.File, rel string) map[*ast.SelectorExpr]bool {
	allowed := map[*ast.SelectorExpr]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		legacy := rel == "internal/store/landing_start.go" && fn.Name.Name == "startLandingWithSide"
		active := rel == "internal/store/side_append.go" && fn.Name.Name == "StartProvenanceLanding"
		dependencies := rel == "internal/store/dependency_start.go" && fn.Name.Name == "StartDependencyLanding"
		if !legacy && !active && !dependencies {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 11 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "startLandingTransaction" {
				return true
			}
			isNil := func(i int) bool { arg, ok := call.Args[i].(*ast.Ident); return ok && arg.Name == "nil" }
			// Positions 9 and 10 are the provenance and dependency evidence.
			if legacy && isNil(9) && isNil(10) || active && isNil(10) || dependencies && isNil(9) && !isNil(10) {
				allowed[sel] = true
			}
			return true
		})
	}
	return allowed
}

func TestActivatedDependencyWriterGuard(t *testing.T) {
	for _, tc := range []struct {
		source, path string
		want         bool
	}{
		{`if w.dependencies != nil { ctx = store.WithDependencyActivation(ctx,w.id,b) }`, "internal/serve/serve.go", true},
		{`if w.dependencies != nil { emit = func(e queue.Event) error { return s.store.StartDependencyLanding() } }`, "internal/serve/worker.go", true},
		{`ctx = store.WithDependencyActivation(ctx,w.id,b)`, "internal/serve/serve.go", false},
		{`if w.provenance != nil { s.store.StartDependencyLanding() }`, "internal/serve/worker.go", false},
		{`if w.dependencies == nil { s.store.StartDependencyLanding() }`, "internal/serve/worker.go", false},
		{`if w.dependencies != nil { _ = s.store.StartDependencyLanding }`, "internal/serve/worker.go", false},
		{`if w.dependencies != nil { s.store.StartDependencyLanding() }`, "cmd/openmerge/main.go", false},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "guard.go", "package serve; func f(){"+tc.source+"}", 0)
		must(t, err)
		if (len(activatedCalls(file, tc.path, "dependencies")) > 0) != tc.want {
			t.Fatal(tc.source)
		}
	}
	for _, tc := range []struct {
		source, path, fn string
		want             bool
	}{
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,nil,&v)`, "internal/store/dependency_start.go", "StartDependencyLanding", true},
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,&p,&v)`, "internal/store/dependency_start.go", "StartDependencyLanding", false},
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,nil,nil)`, "internal/store/landing_start.go", "startLandingWithSide", true},
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,nil,&v)`, "internal/store/landing_start.go", "startLandingWithSide", false},
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,&p,&v)`, "internal/store/side_append.go", "StartProvenanceLanding", false},
		{`s.startLandingTransaction(a,b,c,d,e,f,g,h,i,nil,&v)`, "internal/serve/worker.go", "StartDependencyLanding", false},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "guard.go", "package store; func (s *Store) "+tc.fn+"(){"+tc.source+"}", 0)
		must(t, err)
		if (len(sanctionedLandingTransactions(file, tc.path)) > 0) != tc.want {
			t.Fatal(tc.source, tc.path)
		}
	}
}
