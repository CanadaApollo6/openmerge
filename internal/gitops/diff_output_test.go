package gitops

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDiffOutputBudgetAppliesToPipeCopies(t *testing.T) {
	// An io.Reader without WriterTo forces io.Copy's destination fast path.
	// This is the same boundary used to drain a git subprocess's stdout.
	out := &limitedDiffBuffer{limit: 8}
	n, err := io.Copy(out, struct{ io.Reader }{strings.NewReader("0123456789")})
	if n != 8 || !errors.Is(err, errDiffOutputLimit) || !out.overflow || out.String() != "01234567" {
		t.Fatalf("copy escaped byte budget: n=%d err=%v overflow=%v output=%q", n, err, out.overflow, out.String())
	}
	// Verify the actual os/exec pipe path, not only io.Copy in isolation.
	f := newFixture(t)
	sha := f.diffCommit(f.base, "large subprocess blob", map[string]diffEntry{"large": {mode: "100644", content: "0123456789"}})
	s, unlock, err := f.g.openSession(t.Context(), testRepo, true)
	must(t, err)
	defer unlock()
	must(t, s.commits(t.Context(), sha))
	_, overflow, err := s.diffCommand(t.Context(), 8, "cat-file", "blob", f.base+":base.txt")
	// This blob is short; no false overflow at a normal read.
	if err != nil || overflow {
		t.Fatalf("short blob rejected: %v/%v", overflow, err)
	}
	raw, overflow, err := s.diffCommand(t.Context(), 8, "cat-file", "blob", sha+":large")
	if err == nil || !overflow || raw != "01234567" {
		t.Fatalf("subprocess escaped byte budget: %q/%v/%v", raw, overflow, err)
	}
}
