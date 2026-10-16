package status

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestBatchTimeoutProvenanceEvidenceSurvivesBisection(t *testing.T) {
	events := append(admission(),
		queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 2, At: at},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}, {PR: 2, HeadSHA: "head2"}}, At: at},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: at, Method: queue.Squash},
		queue.CheckProvenanceObserved{BatchID: "b1", TestedSHA: "tested", Checks: []queue.UnboundCheck{{Name: "ci", Source: "app:123"}}},
		queue.BatchFailed{BatchID: "b1", Reason: queue.ChecksTimeout},
	)
	q, entry, why := Project(journal(events...), 1, 1, nil)
	if q.ActiveBatch != nil || entry.Status != "queued" || entry.Reason != "" || entry.Detail != "" {
		t.Fatalf("stale diagnostic survived bisection: %+v", entry)
	}
	for _, text := range []string{"checks-timeout", "unbound-check-provenance", `"ci"`, `"app:123"`, "head_branch=null", "openmerge/queue/main/b1"} {
		if len(why.Events) != 1 || !strings.Contains(why.Events[0].Summary, text) {
			t.Fatalf("batch failure evidence lost %q: %+v", text, why)
		}
	}
}
