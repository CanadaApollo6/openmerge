package classification

import (
	"strings"
	"testing"
)

func TestGoverningPatternCapsAndLazyCompile(t *testing.T) {
	for _, owners := range []bool{false, true} {
		if _, err := treePattern(strings.Repeat("x", MaxTreePatternBytes+1), owners); err == nil {
			t.Fatal("oversized pattern accepted")
		}
	}
	// Files near their entry budgets must not retain compiled regexps at parse.
	owners := parseOwners(strings.Repeat("src/* @alice\n", MaxOwnerEntries))
	if !owners.valid || len(owners.entries) != MaxOwnerEntries {
		t.Fatal("owners corpus rejected")
	}
	attrs := parseAttributes(strings.Repeat("src/* linguist-generated\n", 4096))
	if len(attrs.entries) != 4096 {
		t.Fatal("attributes corpus rejected")
	}
	for _, e := range owners.entries {
		if e.pattern.expression != nil || e.pattern.direct != nil {
			t.Fatal("owners compiled during parse")
		}
	}
	for _, e := range attrs.entries {
		if e.pattern.expression != nil {
			t.Fatal("attributes compiled during parse")
		}
	}
	if !owners.entries[0].pattern.MatchString("src/file") || owners.entries[0].pattern.expression == nil {
		t.Fatal("lazy matcher failed")
	}
	if parseOwners(strings.Repeat("x", MaxTreePatternBytes+1) + " @alice").valid {
		t.Fatal("CODEOWNERS pattern cap bypassed")
	}
	if len(parseAttributes(strings.Repeat("x", MaxTreePatternBytes+1)+" linguist-generated").diagnostics) == 0 {
		t.Fatal("attributes pattern cap bypassed")
	}
}

func TestActivationClassifierVersion(t *testing.T) {
	if Version != "classification-v1/git-evidence-v8-retained-hunk-budget/governing-v2-pattern-budget/hunks-v1/facets-v3" {
		t.Fatal("pattern budget version drift")
	}
	if Version == UnboundedPatternVersion {
		t.Fatal("changed governing semantics need a distinct version")
	}
}
