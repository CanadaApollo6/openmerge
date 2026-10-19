package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// No event compaction or snapshot persistence exists today. Any new SQL that
// can discard journal history requires replacing this guard with an integration
// test proving decisions AND their referenced eligibility reads survive pruning.
func TestDecisionEvidenceRetentionNoPruning(t *testing.T) {
	destructive := regexp.MustCompile(`(?i)\b(delete\s+from|truncate(?:\s+table)?|drop\s+table)\b`)
	check := func(path, text string) {
		if destructive.MatchString(text) {
			t.Errorf("%s introduces history pruning SQL: prove landing_decision_recorded and approval_eligibility_observed retention before replacing this guard", path)
		}
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasSuffix(path, ".sql") {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			check(path, string(raw))
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				s, e := strconv.Unquote(literal.Value)
				if e == nil {
					check(path, s)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
