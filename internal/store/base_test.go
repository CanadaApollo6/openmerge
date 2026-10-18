package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecordBaseMoveAtomicConcurrentAndStale(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	must(t, s.RecordBaseMove(t.Context(), q, 0, "", "base", nil))
	_, watermark, err := s.Load(t.Context(), q)
	must(t, err)
	e := queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "admin", VerificationBeforeLanding: "none", At: epoch}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := s.RecordBaseMove(t.Context(), q, watermark, "base", "admin", &e); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	records, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	if len(records) != 3 {
		t.Fatalf("duplicate/partial observations: %+v", records)
	}
	if _, ok := records[1].Event.(queue.OutsidePushRecorded); !ok {
		t.Fatal("audit event not atomic with BaseMoved")
	}
	if moved, ok := records[2].Event.(queue.BaseMoved); !ok || moved.SHA != "admin" {
		t.Fatal("missing BaseMoved")
	}
	stale := e
	stale.NewSHA = "other"
	if err := s.RecordBaseMove(t.Context(), q, watermark, "base", "other", &stale); !errors.Is(err, ErrBaseObservationStale) {
		t.Fatalf("stale observation accepted: %v", err)
	}
	invalid := e
	invalid.PreviousSHA = "admin"
	invalid.NewSHA = "next"
	invalid.VerificationBeforeLanding = "passed"
	if err := s.RecordBaseMove(t.Context(), q, records[len(records)-1].ID, "admin", "next", &invalid); err == nil {
		t.Fatal("invalid evidence accepted")
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.BaseSHA != "admin" || len(state.RecentOutsidePushes()) != 1 {
		t.Fatal("failed transaction changed base/audit")
	}
}

func TestRecordBaseMoveCannotClassifyUnsettledAttempt(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	_, err := s.Append(t.Context(), q, []queue.Event{
		queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: epoch}, queue.LandingStarted{BatchID: "b1", AttemptID: "a", FenceToken: "fence", At: epoch},
	}, "")
	must(t, err)
	_, watermark, err := s.Load(t.Context(), q)
	must(t, err)
	e := queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "admin", VerificationBeforeLanding: "none", At: epoch}
	if err := s.RecordBaseMove(t.Context(), q, watermark, "base", "admin", &e); !errors.Is(err, ErrBaseObservationStale) {
		t.Fatalf("classified unsettled attempt: %v", err)
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.BaseSHA != "base" || len(state.RecentOutsidePushes()) != 0 {
		t.Fatal("unsettled base changed")
	}
}

func TestRecordBaseMoveRejectsChangedJournalWithSameBase(t *testing.T) {
	for _, events := range [][]queue.Event{
		{queue.BaseMoved{SHA: "other"}, queue.BaseMoved{SHA: "base"}},
		{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}},
	} {
		s := newStore(t)
		q := ensureQueue(t, s, "")
		must(t, s.RecordBaseMove(t.Context(), q, 0, "", "base", nil))
		_, watermark, err := s.Load(t.Context(), q)
		must(t, err)
		_, err = s.Append(t.Context(), q, events, "")
		must(t, err)
		e := queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "admin", VerificationBeforeLanding: "none", At: epoch}
		if err := s.RecordBaseMove(t.Context(), q, watermark, "base", "admin", &e); !errors.Is(err, ErrBaseObservationStale) {
			t.Fatalf("accepted old journal snapshot: %v", err)
		}
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if state.BaseSHA != "base" || len(state.RecentOutsidePushes()) != 0 {
			t.Fatalf("stale observer changed state: %+v", state)
		}
	}
}
