package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func auditEvidence(a *OutsidePushAudit) []queue.OutsidePushRecorded {
	events := make([]queue.OutsidePushRecorded, len(a.Moves))
	for i, m := range a.Moves {
		events[i] = queue.OutsidePushRecorded{PreviousSHA: m.PreviousSHA, NewSHA: m.NewSHA, PossibleStaleObservation: m.PossibleStaleObservation, VerificationBeforeLanding: "none", At: m.At}
	}
	return events
}

func auditPass(t *testing.T, s *Store, q QueueID) *OutsidePushAudit {
	t.Helper()
	a, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	committed, err := s.CommitOutsidePushAudit(t.Context(), q, a, auditEvidence(a))
	must(t, err)
	if !committed {
		t.Fatal("unexpected cursor conflict")
	}
	return a
}

func seedOutsideLanding(t *testing.T, s *Store, q QueueID) queue.State {
	t.Helper()
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch}, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: epoch}, queue.LandingStarted{BatchID: "b1", AttemptID: "a", FenceToken: "fence", At: epoch})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	return state
}

func TestOutsidePushConcurrentAuditorsExactlyOnce(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	auditPass(t, s, q)
	var ids []int64
	for _, sha := range []string{"B", "C", "B"} {
		ids = append(ids, appendEvents(t, s, q, queue.BaseMoved{SHA: sha}))
	}
	appendEvents(t, s, q, queue.RefreshRequested{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			a, err := s.ReadOutsidePushAudit(t.Context(), q)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = s.CommitOutsidePushAudit(t.Context(), q, a, auditEvidence(a))
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	auditPass(t, s, q)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.RecentOutsidePushes()) != 3 {
		t.Fatalf("lost/duplicate transitions: %+v", state.RecentOutsidePushes())
	}
	for _, id := range ids {
		var n int
		must(t, s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1 AND dedupe_key=$2`, q, fmt.Sprintf("outside-base/%d", id)).Scan(&n))
		if n != 1 {
			t.Fatalf("transition %d has %d receipts", id, n)
		}
	}
}

// Round 3's older-receipt/newer-observation race now uses journal positions.
// Exercise both commit orders: the losing cursor retries, preserving B and C.
func TestReviewRound3PeerOlderReceiptDropsNewerObservedTip(t *testing.T) {
	for _, olderFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(olderFirst), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
			auditPass(t, s, q)
			appendEvents(t, s, q, queue.BaseMoved{SHA: "B"})
			older, err := s.ReadOutsidePushAudit(t.Context(), q)
			must(t, err)
			appendEvents(t, s, q, queue.BaseMoved{SHA: "C"})
			newer, err := s.ReadOutsidePushAudit(t.Context(), q)
			must(t, err)
			first, second := older, newer
			if !olderFirst {
				first, second = second, first
			}
			ok, err := s.CommitOutsidePushAudit(t.Context(), q, first, auditEvidence(first))
			must(t, err)
			if !ok {
				t.Fatal("first cursor rejected")
			}
			ok, err = s.CommitOutsidePushAudit(t.Context(), q, second, auditEvidence(second))
			must(t, err)
			if ok {
				t.Fatal("competing cursor silently accepted")
			}
			appendEvents(t, s, q, queue.BaseMoved{SHA: "B"})
			auditPass(t, s, q)
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			pushes := state.RecentOutsidePushes()
			if len(pushes) != 3 || pushes[0].NewSHA != "B" || pushes[1].NewSHA != "C" || pushes[2].NewSHA != "B" {
				t.Fatalf("lost newer durable observation: %+v", pushes)
			}
		})
	}
}

func TestReviewRound3RepairCanBeatStaleReceipt(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	observer, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	staleID := appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"})
	// The repair audits before the old observer commits. No separate marker exists.
	repair := auditPass(t, s, q)
	if len(repair.Moves) != 1 || repair.Moves[0].ID != staleID || !repair.Moves[0].PossibleStaleObservation {
		t.Fatalf("ambiguous rewind not flagged: %+v", repair.Moves)
	}
	ok, err := s.CommitOutsidePushAudit(t.Context(), q, observer, auditEvidence(observer))
	must(t, err)
	if ok {
		t.Fatal("old observer replaced the repair's cursor")
	}
	auditPass(t, s, q)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	pushes := state.RecentOutsidePushes()
	if len(pushes) != 1 || !pushes[0].PossibleStaleObservation || pushes[0].NewSHA != "base" {
		t.Fatalf("repair mistaken for outside landing: %+v", pushes)
	}
}

func TestOutsidePushCursorResumeAfterRestartAndPendingSettlement(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	firstID := appendEvents(t, s, q, queue.BaseMoved{SHA: "away"})
	finalID := appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	a := auditPass(t, s, q)
	if len(a.Moves) != 0 || a.progress.ScannedThrough != finalID || len(a.progress.Pending) != 2 {
		t.Fatalf("classified unsettled attempt: %+v", a)
	}
	// New Store instance, same durable database, no process-local cursor.
	restarted, err := Open(t.Context(), s.pool.Config().ConnString(), WithTestLockScope(s.testLockScope))
	must(t, err)
	defer restarted.Close()
	a = auditPass(t, restarted, q)
	if len(a.Moves) != 0 || len(a.progress.Pending) != 2 {
		t.Fatal("restart lost pending transitions")
	}
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "abandon"})
	a = auditPass(t, restarted, q)
	if len(a.Moves) != 2 || a.Moves[0].ID != firstID || a.Moves[1].ID != finalID || a.progress.AuditedBaseMove != finalID {
		t.Fatalf("cursor skipped pending moves: %+v", a)
	}
	a = auditPass(t, restarted, q)
	if len(a.Moves) != 0 {
		t.Fatal("restart repeated audits")
	}
}

func TestOutsidePushIncrementalScanAndBoundedPass(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	for range outsidePushScanLimit + 3 {
		appendEvents(t, s, q, queue.RefreshRequested{})
	}
	moveID := appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	a := auditPass(t, s, q)
	if a.progress.ScannedThrough >= moveID {
		t.Fatal("unbounded initial scan")
	}
	a = auditPass(t, s, q)
	if a.progress.ScannedThrough != moveID || a.progress.AuditedBaseMove != moveID {
		t.Fatal("suffix cursor failed")
	}
	// An old undecodable row proves subsequent passes do not decode a prefix.
	_, err := s.pool.Exec(t.Context(), `UPDATE events SET payload='{"type":"refresh_requested","version":999,"data":{}}' WHERE queue_id=$1 AND id < $2`, q, moveID)
	must(t, err)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "next"})
	a = auditPass(t, s, q)
	if len(a.Moves) != 1 || a.Moves[0].PreviousSHA != "base" {
		t.Fatalf("did not resume incrementally: %+v", a.Moves)
	}
	raw, err := s.Observation(t.Context(), q, outsidePushCursorKey)
	must(t, err)
	var cursor outsidePushProgress
	must(t, json.Unmarshal(raw, &cursor))
	if cursor.AuditedBaseMove != a.Moves[0].ID {
		t.Fatal("durable base move cursor not advanced")
	}
}

func TestOutsidePushOwnLandingAndPostLandingTransition(t *testing.T) {
	for _, abandon := range []bool{false, true} {
		t.Run(fmt.Sprint(abandon), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			seedOutsideLanding(t, s, q)
			appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
			auditPass(t, s, q)
			if abandon {
				appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "abandon"})
			} else {
				appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
			}
			a := auditPass(t, s, q)
			previous := "tested"
			if abandon {
				previous = "base"
			}
			if len(a.Moves) != 1 || a.Moves[0].PreviousSHA != previous || a.Moves[0].NewSHA != "outside" {
				t.Fatalf("wrong settlement baseline: %+v", a.Moves)
			}
		})
	}
}

func TestOutsidePushEvidenceMustMatchDurableMove(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "next"})
	a, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	events := auditEvidence(a)
	events[0].NewSHA = "invented"
	if _, err := s.CommitOutsidePushAudit(t.Context(), q, a, events); err == nil {
		t.Fatal("accepted invented transition")
	}
}

func TestOutsidePushRewindToPreviousOwnLandingStillFlagged(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: epoch}, queue.BatchStarted{BatchID: "b2", BaseSHA: "tested", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head2"}}, At: epoch}, queue.TestRefBuilt{BatchID: "b2", TestedSHA: "tested2", Method: queue.Merge, At: epoch}, queue.LandingStarted{BatchID: "b2", AttemptID: "a2", FenceToken: "landed", At: epoch}, queue.Landed{BatchID: "b2", NewBaseSHA: "tested2", NewFence: "landed2"}, queue.BaseMoved{SHA: "tested"}, queue.BaseMoved{SHA: "tested2"})
	a := auditPass(t, s, q)
	if len(a.Moves) != 1 || !a.Moves[0].PossibleStaleObservation || a.Moves[0].PreviousSHA != "tested2" || a.Moves[0].NewSHA != "tested" {
		t.Fatalf("earlier own SHA hid a later rewind: %+v", a.Moves)
	}
}
