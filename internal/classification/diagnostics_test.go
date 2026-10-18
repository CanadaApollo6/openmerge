package classification

import (
	"strings"
	"testing"
)

func TestSourceDiagnosticsBounded(t *testing.T) {
	for name, rows := range map[string][]string{"attributes": parseAttributes(strings.Repeat("[attr]macro invalid\n", 100)).diagnostics, "owners": parseOwners(strings.Repeat("x @bad!\n", 100)).diagnostics} {
		if len(rows) != 9 || !strings.HasPrefix(rows[8], "+") || !strings.HasSuffix(rows[8], " more") {
			t.Fatalf("%s diagnostics=%v", name, rows)
		}
	}
}
