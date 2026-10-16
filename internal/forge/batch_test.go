package forge_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestParseBatchMessage(t *testing.T) {
	for _, tc := range []struct{ name, message, want string }{
		{"authoritative", "Subject\n\nProse\n\nSigned-off-by: A <a@example.test>\nOpenMerge-Batch: b1\n", "b1"},
		{"casing and CRLF", "Subject\r\n\r\nopenmerge-BATCH \t: b1\r\n", "b1"},
		{"other continuation", "Subject\n\nSigned-off-by: A\n <a@example.test>\nOpenMerge-Batch: b1", "b1"},
		{"none", "Subject", ""},
		{"subject only", "OpenMerge-Batch: b1", ""},
		{"no separator", "Subject\nOpenMerge-Batch: b1", ""},
		{"prose", "Subject\n\nOpenMerge-Batch: b1\n\nLater prose", ""},
		{"mixed prose", "Subject\n\nProse\nOpenMerge-Batch: b1", ""},
		{"duplicate", "Subject\n\nOpenMerge-Batch: b1\nOpenMerge-Batch: b2", ""},
		{"duplicate same ID", "Subject\n\nOpenMerge-Batch: b1\nopenmerge-batch: b1", ""},
		{"earlier key", "Subject\n\nOpenMerge-Batch: b1\n\nOpenMerge-Batch: b2", ""},
		{"empty", "Subject\n\nOpenMerge-Batch: \t", ""},
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
