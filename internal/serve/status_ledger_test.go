package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLedgerSummaryCacheTracksWatermarkAndCurrentPolicy(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig+"    speculation:\n      max_depth: 4\n")
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	first, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	cached := w.ledgerCache
	second, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if first != second || w.ledgerCache != cached {
		t.Fatal("unchanged history was rebuilt")
	}
	w.cfg.Speculation.MaxDepth = 8
	_, err = s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if w.ledgerCache != cached || w.ledgerCache.projection.Window != 8 {
		t.Fatal("cached history hid current configuration")
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	b := state.Active
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Terminal: true, Success: true, OriginalDeadline: time.Now().Add(time.Hour), Timeout: time.Hour, Runs: []queue.CheckRun{{ID: 42, Attempt: 1, Name: "ci", Source: "fake", State: "success"}}}}, "")
	must(t, err)
	summary, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if w.ledgerCache == cached || !strings.Contains(summary, "ci/fake#42.1=success") {
		t.Fatalf("new history missing from cached summary: %s", summary)
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "attempt", FenceToken: state.Fence, At: time.Now()}, queue.LandingCleanupPolicyRecorded{BatchID: b.ID, AttemptID: "attempt"}, queue.Landed{BatchID: b.ID, NewBaseSHA: b.TestedSHA, NewFence: "next"}}, "")
	must(t, err)
	summary, err = s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	if len(w.ledgerCache.projection.Entries) != 0 || !strings.Contains(summary, "ci/fake#42.1=success") {
		t.Fatalf("terminal history lost: %s", summary)
	}
}
