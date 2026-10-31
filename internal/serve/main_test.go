package serve

import (
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// Test binaries carry no VCS revision, so activation receipts use the same
// explicit fallback as release tooling. Set it once before any test runs:
// production activation reads it, and per-test restores race with parallel
// tests. Never replace a revision already embedded by Go.
func TestMain(m *testing.M) {
	if authority.RunningBinaryRevision() == "" {
		authority.BuildRevision = "activation-test-revision"
	}
	os.Exit(m.Run())
}
