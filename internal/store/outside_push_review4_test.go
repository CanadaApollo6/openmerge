package store

import (
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"testing"
)

func TestReview4ReturnToLandingAfterOutsideAdvance(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
	auditPass(t, s, q)
	id := appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"})
	a := auditPass(t, s, q)
	if len(a.Moves) != 1 || a.Moves[0].ID != id {
		t.Fatalf("real outside rollback outside -> tested lost: moves=%+v", a.Moves)
	}
}
func TestReview4DeferredObserverAndPeerRepairBothMethods(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch}, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: method, At: epoch}, queue.LandingStarted{BatchID: "b1", AttemptID: "a", FenceToken: "fence", At: epoch})
			observer, err := s.ReadOutsidePushAudit(t.Context(), q)
			must(t, err)
			landed := "tested"
			if method == queue.FastForward {
				landed = "head"
			}
			appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: landed, NewFence: "landed"}, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: landed})
			auditPass(t, s, q)
			ok, err := s.CommitOutsidePushAudit(t.Context(), q, observer, auditEvidence(observer))
			must(t, err)
			if ok {
				t.Fatal("stale cursor accepted")
			}
			auditPass(t, s, q)
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			p := state.RecentOutsidePushes()
			if len(p) != 1 || !p[0].PossibleStaleObservation || p[0].NewSHA != "base" {
				t.Fatalf("false peer landing: %+v", p)
			}
		})
	}
}
func TestReview4PagedPendingCursorSurvivesPeerRestart(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
	noise := make([]queue.Event, 600)
	for i := range noise {
		noise[i] = queue.RefreshRequested{}
	}
	appendEvents(t, s, q, noise...)
	final := appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	var scanned, audited int64
	for i := 0; i < 4; i++ {
		peer, err := Open(t.Context(), s.pool.Config().ConnString(), WithTestLockScope(s.testLockScope))
		must(t, err)
		a := auditPass(t, peer, q)
		peer.Close()
		if a.progress.ScannedThrough < scanned || a.progress.AuditedBaseMove < audited {
			t.Fatal("cursor regressed")
		}
		if a.progress.ScannedThrough < final && len(a.Moves) != 0 {
			t.Fatal("classified before settlement page")
		}
		scanned, audited = a.progress.ScannedThrough, a.progress.AuditedBaseMove
	}
	var n int
	must(t, s.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE queue_id=$1 AND type='outside_push_recorded'`, q).Scan(&n))
	if n != 1 {
		t.Fatalf("pending transition recorded %d times", n)
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	p := state.RecentOutsidePushes()
	if len(p) != 1 || p[0].PreviousSHA != "tested" || p[0].NewSHA != "outside" {
		t.Fatalf("wrong pending classification %+v", p)
	}
}
