package forge_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestParseBatchMessage(t *testing.T) {
	for _, tc := range []struct{ name, message, want string }{
		{"authoritative", "Subject\n\nProse\n\nSigned-off-by: A <a@example.test>\nOpenMerge-Batch: b1\n", "b1"},
		{"identifier alphabet", "Subject\n\nOpenMerge-Batch: AZaz09._-", "AZaz09._-"},
		{"other continuation", "Subject\n\nSigned-off-by: A\n <a@example.test>\nOpenMerge-Batch: b1", "b1"},
		{"lookalike key", "Subject\n\nSіgned-off-by: A\nOpenMerge-Batch: b1", "b1"},
		{"orphan continuation", "Subject\n\n orphan\nOpenMerge-Batch: b1", "b1"},
		{"comment before marker", "Subject\n\n# comment\nOpenMerge-Batch: b1", "b1"},
		{"patch divider is not batch grammar", "Subject\n\n---\nOpenMerge-Batch: b1", "b1"},
		{"none", "Subject", ""},
		{"marker only", "OpenMerge-Batch: b1", "b1"},
		{"no separator", "Subject\nOpenMerge-Batch: b1", "b1"},
		{"prose", "Subject\n\nOpenMerge-Batch: b1\n\nLater prose", ""},
		{"mixed prose", "Subject\n\nProse\nOpenMerge-Batch: b1", "b1"},
		{"duplicate", "Subject\n\nOpenMerge-Batch: b1\nOpenMerge-Batch: b2", ""},
		{"duplicate same ID", "Subject\n\nOpenMerge-Batch: b1\nopenmerge-batch: b1", ""},
		{"earlier key", "Subject\n\nOpenMerge-Batch: b1\n\nOpenMerge-Batch: b2", ""},
		{"earlier malformed key", "Subject\n\n \tOpEnMeRgE-BaTcH = forged\n\nOpenMerge-Batch: b1", ""},
		{"earlier prefixed key", "OpenMerge-BatchExtra: forged\n\nOpenMerge-Batch: b1", ""},
		{"earlier unicode whitespace", "Subject\n\n\u2003openmerge-batch: forged\n\nOpenMerge-Batch: b1", ""},
		{"empty", "Subject\n\nOpenMerge-Batch: \t", ""},
		{"wrong case", "Subject\n\nopenmerge-BATCH: b1", ""},
		{"key whitespace", "Subject\n\nOpenMerge-Batch \t: b1", ""},
		{"missing value space", "Subject\n\nOpenMerge-Batch:b1", ""},
		{"extra value space", "Subject\n\nOpenMerge-Batch:  b1", ""},
		{"trailing value space", "Subject\n\nOpenMerge-Batch: b1 ", ""},
		{"space in ID", "Subject\n\nOpenMerge-Batch: b 1", ""},
		{"slash in ID", "Subject\n\nOpenMerge-Batch: b/1", ""},
		{"unicode ID", "Subject\n\nOpenMerge-Batch: b\u0431", ""},
		{"trailing blank line", "Subject\n\nOpenMerge-Batch: b1\n\n", ""},
		{"trailing comment", "Subject\n\nOpenMerge-Batch: b1\n# comment", ""},
		{"CRLF", "Subject\r\n\r\nOpenMerge-Batch: b1\r\n", "b1"},
		{"continuation", "Subject\n\nOpenMerge-Batch: b1\n b2", ""},
		{"indented key", "Subject\n\n OpenMerge-Batch: b1", ""},
		{"nul", "Subject\n\nOpenMerge-Batch: b1\x00", ""},
		{"embedded CR", "Subject\n\nOpenMerge-Batch: b1\rb2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := forge.ParseBatchMessage(tc.message); got != tc.want {
				t.Fatalf("batch = %q, want %q", got, tc.want)
			}
		})
	}
}
