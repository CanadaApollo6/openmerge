package gitops

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestProvenanceAttributionRealGit(t *testing.T) {
	f := newProvenanceFixture(t)
	first := f.commit("first\n\nAgent-Session: a:one\nEntire-Checkpoint: abcdef012345\n", []string{f.root})
	second := f.commit("second\n\nAGENT-SESSION: a:one\nAgent-Session: a:two\nOpenMerge-Batch: forged\n", []string{first})
	inv := f.read(f.request(second, f.root, f.root), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, first, second)
	existing := []string{"Co-authored-by: Human <human@example.test>", "Signed-off-by: Human <human@example.test>", "OpenMerge-Change: trusted", "OpenMerge-Batch: trusted-batch"}
	block, status := provenance.RetainAttribution(existing, inv)
	if status.Outcome != provenance.ReadComplete || block[len(block)-1] != existing[len(existing)-1] {
		t.Fatalf("block = %v, status %+v", block, status)
	}
	for _, line := range []string{"agent-session: a:one", "agent-session: a:two", "entire-checkpoint: abcdef012345"} {
		if count := slices.Index(block, line); count < 0 {
			t.Fatalf("missing %s", line)
		}
	}
	if strings.Count(strings.Join(block, "\n"), "agent-session: a:one") != 1 || strings.Contains(strings.Join(block, "\n"), "forged") {
		t.Fatal("duplicate or contributor metadata retained")
	}
	// A generated squash/merge message has one final block despite markdown
	// dividers in its prose, and Git reads every retained declaration.
	message := "generated\n\nbody\n---\n\n" + strings.Join(block, "\n") + "\n"
	oid := f.commit(message, []string{f.root})
	oracle := gitCommand(t, f.dir, "", nil, "log", "-1", "--format=%(trailers:only,unfold)", oid)
	for _, line := range block {
		if !strings.Contains(oracle, line) {
			t.Fatalf("Git lost retained attribution %q: %s", line, oracle)
		}
	}
}

func TestProvenanceAttributionBoundariesAndTerminalDiagnostics(t *testing.T) {
	f := newProvenanceFixture(t)
	head := f.commit("subject\n\nAgent-Session: a:one\n", []string{f.root})
	inv := f.read(f.request(head, f.root, f.root), provenance.DefaultInventoryLimits())
	expectInventoryIDs(t, inv, head)
	agent := "agent-session: a:one"
	// Every newline and byte of existing sign-off content counts.
	exact := "Signed-off-by: " + strings.Repeat("x", provenance.MaxAttributionBytes-len("Signed-off-by: ")-len(agent)-2)
	block, status := provenance.RetainAttribution([]string{exact}, inv)
	if status.Outcome != provenance.ReadComplete || len(strings.Join(block, "\n")+"\n") != provenance.MaxAttributionBytes {
		t.Fatalf("exact bound = %+v", status)
	}
	// Write the exact boundary as a real commit and compare Git's own output.
	oid := f.commit("generated\n\n"+strings.Join(block, "\n")+"\n", []string{f.root})
	oracle := gitCommand(t, f.dir, "", nil, "log", "-1", "--format=%(trailers:only,unfold)", oid)
	if oracle != strings.Join(block, "\n") {
		t.Fatal("boundary block differs from Git commit reader")
	}
	block, status = provenance.RetainAttribution([]string{exact + "x"}, inv)
	if status.Outcome != provenance.LimitExceeded || status.Resource != "attribution_bytes" || block != nil {
		t.Fatalf("overflow silently truncated: %+v", status)
	}
	// UTF-8 is measured in bytes, not runes.
	block, status = provenance.RetainAttribution([]string{"Signed-off-by: " + strings.Repeat("é", provenance.MaxAttributionBytes/2)}, inv)
	if status.Outcome != provenance.LimitExceeded || block != nil {
		t.Fatal("UTF-8 bypassed byte bound")
	}
	// Many small valid declarations can exceed the total attribution ceiling
	// while remaining inside commit/message/source/declaration read ceilings.
	tip := f.root
	for i := range 100 {
		var lines []string
		for j := range 8 {
			lines = append(lines, fmt.Sprintf("Agent-Session: source:s%d-%d-%s", i, j, strings.Repeat("x", 100)))
		}
		tip = f.commit("many\n\n"+strings.Join(lines, "\n")+"\n", []string{tip})
	}
	many := f.read(f.request(tip, f.root, f.root), provenance.DefaultInventoryLimits())
	if !many.Complete {
		t.Fatalf("source inventory failed: %+v", many.Diagnostic)
	}
	block, status = provenance.RetainAttribution(existingAttribution(), many)
	if status.Outcome != provenance.LimitExceeded || block != nil {
		t.Fatalf("large retention = %+v", status)
	}
	// Overlong/invalid values remain agent declarations with digests. The
	// attribution helper fails instead of inventing a shorter valid value.
	for _, value := range []string{strings.Repeat("x", 200), "a:\xff"} {
		tip = f.commit("invalid\n\nAgent-Session: "+value+"\n", []string{f.root})
		bad := f.read(f.request(tip, f.root, f.root), provenance.DefaultInventoryLimits())
		expectInventoryIDs(t, bad, tip)
		found := false
		for _, d := range bad.Commits[0].Declarations {
			if d.Kind == "agent-session" {
				found = d.Class == provenance.Agent && d.Value == "" && d.ValueDigest != ""
			}
		}
		if !found {
			t.Fatal("invalid agent evidence disappeared")
		}
		block, status = provenance.RetainAttribution(nil, bad)
		if block != nil || (status.Outcome != provenance.LimitExceeded && status.Outcome != provenance.InvalidInput) {
			t.Fatalf("invalid value became valid: %+v", status)
		}
	}
	inv.Complete = false
	if block, status = provenance.RetainAttribution(nil, inv); block != nil || status.Outcome != provenance.ReadPending {
		t.Fatal("incomplete retention passed")
	}
}

func existingAttribution() []string {
	return []string{"Signed-off-by: Human <human@example.test>", "OpenMerge-Batch: trusted"}
}
