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

var decisionEvidenceRetentionSQL = regexp.MustCompile(
	`(?is)\b(?:DELETE\s+FROM\s+(?:ONLY\s+)?(?:[a-z_][\w]*\.)?[a-z_][\w]*(?:\s+(?:AS\s+)?[a-z_][\w]*)?\s*(?:WHERE\b|USING\b|RETURNING\b|;|$)` +
		`|TRUNCATE(?:\s+TABLE)?\s+(?:ONLY\s+)?(?:[a-z_][\w]*\.)?[a-z_][\w]*(?:\s*,\s*(?:[a-z_][\w]*\.)?[a-z_][\w]*)*(?:\s+(?:CONTINUE|RESTART)\s+IDENTITY)?(?:\s+(?:CASCADE|RESTRICT))?\s*(?:;|$)` +
		`|DROP\s+(?:TABLE|INDEX)(?:\s+CONCURRENTLY)?\s+(?:IF\s+EXISTS\s+)?(?:[a-z_][\w]*\.)?[a-z_][\w]*(?:\s+(?:CASCADE|RESTRICT))?\s*(?:;|$)` +
		`|UPDATE\s+(?:ONLY\s+)?(?:[a-z_][\w]*\.)?events(?:\s+(?:AS\s+)?[a-z_][\w]*)?\s+SET\s+[^;]*\bpayload\s*=)`,
)

// No event compaction or snapshot persistence exists today. This source-level
// sentinel scans SQL literals in production Go under internal/ and cmd/ plus
// checked-in SQL there. It cannot detect dynamically assembled SQL, direct
// database changes, or SQL issued by external clients. Any new pruning path
// requires integration coverage proving decisions AND their referenced reads
// survive retention.
func TestDecisionEvidenceRetentionNoPruning(t *testing.T) {
	check := func(path, text string) {
		if decisionEvidenceRetentionSQL.MatchString(text) {
			t.Errorf("%s introduces history pruning SQL: prove landing_checks_recorded, landing_decision_recorded and approval_eligibility_observed retention before replacing this guard", path)
		}
	}
	repoRoot := filepath.Join("..", "..")
	for _, sourceDir := range []string{"internal", "cmd"} {
		root := filepath.Join(repoRoot, sourceDir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
}

func TestDecisionEvidenceRetentionGuardRecognizesHistoryMutation(t *testing.T) {
	for _, sql := range []string{
		`DROP INDEX events_reporting_pr_alias`,
		`TRUNCATE events`,
		`TRUNCATE TABLE public.events;`,
		`DELETE FROM events WHERE queue_id=7`,
		`UPDATE events SET payload='{}' WHERE id=1`,
		`UPDATE internal.events AS e SET payload='{}' WHERE e.id=1`,
	} {
		if !decisionEvidenceRetentionSQL.MatchString(sql) {
			t.Errorf("retention guard did not recognize %q", sql)
		}
	}
	for _, prose := range []string{
		`Do not truncate the final summary across a warning boundary.`,
		`The journal should not be deleted from the queue during a read.`,
		`A table drop would lose the reviewer history.`,
		`Drop index usage from the analysis if the plan is unclear.`,
	} {
		if decisionEvidenceRetentionSQL.MatchString(prose) {
			t.Errorf("retention guard treated prose as SQL: %q", prose)
		}
	}
}
