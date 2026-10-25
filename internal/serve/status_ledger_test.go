package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLedgerSummaryCacheTracksWatermarkAndCurrentPolicy(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig+"    speculation:\n      max_depth: 4\n")
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		first, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if !strings.Contains(first, "Current state: Waiting for CI on batch b1") {
			t.Fatalf("missing waiting reason: %s", first)
		}
		beforePulls, beforeChecks := w.Driver.(*countingDriver).pulls, w.Driver.(*countingDriver).checks
		cached := w.ledgerCache
		second, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if d := w.Driver.(*countingDriver); d.pulls != beforePulls || d.checks != beforeChecks {
			t.Fatal("waiting presentation observed the forge")
		}
		if first != second || w.ledgerCache != cached {
			t.Fatal("unchanged history was rebuilt")
		}
		w.cfg.Speculation.MaxDepth = 8
		_, err = s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if w.ledgerCache != cached || w.ledgerCache.projection.Window != 4 {
			t.Fatal("raising the ceiling reset W or rebuilt unchanged history")
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
	})
}

func TestAdaptiveSummarySurvivesLongDiagnosticHeader(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig+"    speculation: {max_depth: 4}\n")
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		b := state.Active
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.BatchFailed{BatchID: b.ID, Reason: queue.ChecksTimeout}}, "")
		must(t, err)
		suffix, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		// The forge bounds each line to 1024 runes. Queue inputs get their own line
		// so a long pause/provenance diagnostic cannot truncate them away.
		text := diagnostic.NewSanitizer().SanitizeText(strings.Repeat("x", 2048) + suffix)
		for _, want := range []string{"Queue: window: 2", "batch size: 1", "p̂: n/a (0 samples)", "last window failure: revision", "b1 checks-timeout"} {
			if !strings.Contains(text, want) {
				t.Fatalf("summary lost %q after bounding: %s", want, text)
			}
		}
	})
}

func TestWaitingCheckDoesNotWriteForQueuePosition(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, reads := fixture(t, repositoryConfig)
		must(t, s.cycle(t.Context(), w))
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		for n := 1; n <= 3; n++ {
			head := w.prs[1].HeadSHA
			if n > 1 {
				pr, openErr := f.OpenPR(repo, n, state.BaseSHA, true)
				must(t, openErr)
				head = pr.HeadSHA
			}
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.PRObserved{PR: n, HeadSHA: head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: n, At: time.Now()}}, "")
			must(t, err)
		}
		writes := &instructionDriver{Driver: w.Driver}
		w.Driver = writes
		must(t, s.report(t.Context(), w))
		before := writes.writes
		pulls, checks := reads.pulls, reads.checks
		state, _, err = s.store.Load(t.Context(), w.id)
		must(t, err)
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.Dequeued{PR: 2, AdmissionID: state.Entries[1].AdmissionID, Reason: queue.Requested}}, "")
		must(t, err)
		must(t, s.report(t.Context(), w))
		if writes.writes != before || reads.pulls != pulls || reads.checks != checks {
			t.Fatalf("position-only change added forge calls: writes %d -> %d", before, writes.writes)
		}
		if got := f.Statuses(repo, 3); len(got) != 1 {
			t.Fatalf("missing queued status: %+v", got)
		}
	})
}

func TestWaitingCheckProjectionRedactsBeforeBounds(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig)
		secret := "opaque_secret*`value`"
		s.options.Sanitizer = diagnostic.NewSanitizer(secret)
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		must(t, s.cycle(t.Context(), w))
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		b := state.Active
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Timeout: time.Hour, OriginalDeadline: time.Now().Add(time.Hour), Runs: []queue.CheckRun{{ID: 42, Name: secret, Source: "fake", State: "pending"}}}}, "")
		must(t, err)
		got, err := s.ledgerSummary(t.Context(), w, 1)
		must(t, err)
		if strings.Contains(got, "opaque") || strings.Contains(got, "value") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("secret in check summary: %s", got)
		}
	})
}
