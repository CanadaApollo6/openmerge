package serve

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCheckRetryStatusIncludesPassedFlakyIdentity(t *testing.T) {
	got := checkRetryStatus(&queue.CheckRetryState{
		Dispatched: true,
		RerunState: "passed",
		FailedRuns: []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}},
	})
	for _, want := range []string{"check retry passed", "1 flaky original", "ci/app:ci#7.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status=%q missing %q", got, want)
		}
	}
}
