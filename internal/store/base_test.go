package store

import (
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func outsideEvidence(previous, tip string) queue.OutsidePushRecorded {
	return queue.OutsidePushRecorded{PreviousSHA: previous, NewSHA: tip, VerificationBeforeLanding: "none", At: epoch}
}

func TestRecordBaseMoveAuditConcurrentAndUnrelatedJournal(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	basis, _, err := s.Load(t.Context(), q)
	must(t, err)
	appendEvents(t, s, q, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true})
	moveID := appendEvents(t, s, q, queue.BaseMoved{SHA: "admin"})
	appendEvents(t, s, q, queue.RefreshRequested{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := s.RecordBaseMove(t.Context(), q, basis, moveID, outsideEvidence("base", "admin")); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.BaseSHA != "admin" || len(state.RecentOutsidePushes()) != 1 {
		t.Fatalf("duplicate/missing audit: %+v", state.RecentOutsidePushes())
	}
}

func TestRecordBaseMoveSkipsStaleAuditWithoutChangingBase(t *testing.T) {
	for _, change := range []queue.Event{queue.BaseMoved{SHA: "peer"}, queue.FenceInitialized{Token: "peer"}} {
		s := newStore(t)
		q := ensureQueue(t, s, "")
		appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
		basis, _, err := s.Load(t.Context(), q)
		must(t, err)
		appendEvents(t, s, q, change)
		moveID := appendEvents(t, s, q, queue.BaseMoved{SHA: "admin"})
		must(t, s.RecordBaseMove(t.Context(), q, basis, moveID, outsideEvidence("base", "admin")))
		state, _, err := s.Load(t.Context(), q)
		must(t, err)
		if state.BaseSHA != "admin" || len(state.RecentOutsidePushes()) != 0 {
			t.Fatal("audit changed reconciliation")
		}
	}
}

func seedOutsideLanding(t *testing.T, s *Store, q QueueID) queue.State {
	t.Helper()
	appendEvents(t, s, q,
		queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: epoch}, queue.LandingStarted{BatchID: "b1", AttemptID: "a", FenceToken: "fence", At: epoch})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	return state
}

func TestRecordLandingRewriteTransitionsAndUnknownAncestry(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	basis := seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.RefreshRequested{})
	for _, pair := range [][2]string{{"tested", "back"}, {"tested", "back"}, {"back", "next"}, {"back", "next"}, {"next", "back"}, {"back", "next"}} {
		must(t, s.RecordLandingRewrite(t.Context(), q, basis, outsideEvidence(pair[0], pair[1])))
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Landing || state.Fence != basis.Fence || state.BaseSHA != basis.BaseSHA || len(state.RecentOutsidePushes()) != 4 {
		t.Fatalf("audit affected settlement or lost transition: %+v", state)
	}
	tip, err := s.LandingRewriteTip(t.Context(), q, "a")
	must(t, err)
	if tip != "next" {
		t.Fatalf("latest tip %s", tip)
	}
	appendEvents(t, s, q, queue.BaseMoved{SHA: "peer"})
	must(t, s.RecordLandingRewrite(t.Context(), q, basis, outsideEvidence("next", "stale")))
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if len(state.RecentOutsidePushes()) != 4 {
		t.Fatal("stale landing observation accepted")
	}
}

func TestRecordAbandonedBaseMoveDurableAwayAndBack(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "away"}, queue.BaseMoved{SHA: "base"})
	moves, err := s.AbandonedBaseMoves(t.Context(), q)
	must(t, err)
	if len(moves) != 0 {
		t.Fatal("classified an unsettled attempt")
	}
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "abandon"})
	moves, err = s.AbandonedBaseMoves(t.Context(), q)
	must(t, err)
	if len(moves) != 2 || moves[0].PreviousSHA != "base" || moves[0].NewSHA != "away" || moves[1].PreviousSHA != "away" || moves[1].NewSHA != "base" {
		t.Fatalf("lost individual transitions: %+v", moves)
	}
	for range 2 {
		for _, move := range moves {
			must(t, s.RecordAbandonedBaseMove(t.Context(), q, move, outsideEvidence(move.PreviousSHA, move.NewSHA)))
		}
	}
	invalid := moves[0]
	invalid.NewSHA = "invented"
	if s.RecordAbandonedBaseMove(t.Context(), q, invalid, outsideEvidence(invalid.PreviousSHA, invalid.NewSHA)) == nil {
		t.Fatal("accepted invented history")
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.Fence != "abandon" || state.BaseSHA != "base" || len(state.RecentOutsidePushes()) != 2 {
		t.Fatal("audit changed settlement or duplicated records")
	}
}

func TestRecordBaseMoveSkipsStalePeerTipAndRepair(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.RefreshRequested{})
	basis := seedOutsideLanding(t, s, q)
	for range 10 {
		appendEvents(t, s, q, queue.RefreshRequested{})
	}
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"})
	moveID := appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	must(t, s.RecordBaseMove(t.Context(), q, basis, moveID, queue.OutsidePushRecorded{}))
	basis, _, err := s.Load(t.Context(), q)
	must(t, err)
	moveID = appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"})
	must(t, s.RecordBaseMove(t.Context(), q, basis, moveID, outsideEvidence("base", "tested")))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.RecentOutsidePushes()) != 0 {
		t.Fatal("peer landing classified as outside")
	}
}
