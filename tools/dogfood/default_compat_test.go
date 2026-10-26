package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/google/go-github/v93/github"
)

// These goldens were captured before configurable CI workflows, using the
// existing journal, git, Actions, and comparison fixtures.
func TestDefaultOutputCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no flag", nil},
		{"explicit default", []string{"--ci-workflow", defaultCIWorkflow}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			var o options
			o.flags(fs)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			defaultOutputCompatibility(t, o.ciWorkflows)
		})
	}
}

func defaultOutputCompatibility(t *testing.T, workflows ciWorkflows) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_DATE", "2026-10-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-10-01T00:00:00Z")
	g := newGitFixture(t)
	base := g.commit("base")
	head := g.commit("head", base)
	f := auditFixture(t, queue.Merge, base, head, head, head)
	first := runFixture(1, 1, 10, 11, 15, head)
	first.Conclusion = new("failure")
	latest := runFixture(1, 2, 10, 16, 20, head)
	ignored := runFixture(2, 1, 10, 11, 20, head)
	ignored.Path = new(".github/workflows/unrelated.yml")
	a := &fakeActions{runs: []*github.WorkflowRun{latest, latest, ignored}, historical: map[[2]int64]*github.WorkflowRun{{1, 1}: first}}
	result := auditWorkflowsTest(t, workflows, f, g, a)
	var out bytes.Buffer
	if err := writeJSON(&out, result); err != nil {
		t.Fatal(err)
	}
	defaultGolden(t, "audit.json", out.Bytes())
	r, err := reportJournalAt(f.j, testWindow(0, 60), epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	r.Runs, err = countRuns(t.Context(), workflows, a, "main", r.Batches)
	if err != nil {
		t.Fatal(err)
	}
	finishDrain(&r, f.j)
	if err := attachAudit(&r, f.j, result); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := writeJSON(&out, r); err != nil {
		t.Fatal(err)
	}
	defaultGolden(t, "report.json", out.Bytes())
	out.Reset()
	if err := printReport(&out, r); err != nil {
		t.Fatal(err)
	}
	defaultGolden(t, "report.txt", out.Bytes())
	latest.Conclusion = new("failure")
	result = auditWorkflowsTest(t, workflows, f, g, a)
	out.Reset()
	if err := writeJSON(&out, result); err != nil {
		t.Fatal(err)
	}
	defaultGolden(t, "audit-failure.json", out.Bytes())

	ra, rb := comparableReports()
	dir := t.TempDir()
	for name, report := range map[string]Report{"a.json": ra, "b.json": rb} {
		out.Reset()
		if err := writeJSON(&out, report); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, asJSON := range []bool{false, true} {
		args, name := []string{"compare"}, "compare.txt"
		if asJSON {
			args, name = append(args, "--json"), "compare.json"
		}
		args = append(args, filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json"))
		out.Reset()
		var errs bytes.Buffer
		if code := run(t.Context(), args, &out, &errs); code != 0 {
			t.Fatalf("compare exited %d: %s", code, &errs)
		}
		defaultGolden(t, name, out.Bytes())
	}
}

func defaultGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata/default", name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("default output changed from pre-workflow-selection %s\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}
