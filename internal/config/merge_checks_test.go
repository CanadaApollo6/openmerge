package config

import (
	"strings"
	"testing"
)

func TestMergeRequiresCheckPredicate(t *testing.T) {
	for _, fields := range []string{"", "    merge_when: []\n", "    merge_when: [base = main]\n", "    enter_when: [check-success = ci]\n    merge_when: [{not: draft}]\n"} {
		t.Run(fields, func(t *testing.T) {
			_, _, err := Load([]byte("version: 1\nqueues:\n  - name: release\n" + fields))
			if err == nil || !strings.Contains(err.Error(), `queue "release"`) || !strings.Contains(err.Error(), "check-success = ci") {
				t.Fatalf("missing actionable check validation: %v", err)
			}
		})
	}
}
