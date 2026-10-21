package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

func TestOutsidePushRejectedAuthorityCursorRescansWithoutDuplicateReceipts(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "B"})
	auditPass(t, s, q) // A receipt already exists before the cursor is rejected.
	for range outsidePushScanLimit {
		appendEvents(t, s, q, queue.RefreshRequested{})
	}
	lastMove := appendEvents(t, s, q, queue.BaseMoved{SHA: "C"}, queue.BaseMoved{SHA: "D"})

	// Model a v7 cursor written before raw activation provenance was rejected.
	raw, err := os.ReadFile("../queue/testdata/wire/v1/variants/policy_authority_applied_activation.json")
	must(t, err)
	event, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := event.(queue.PolicyAuthorityApplied)
	state := queue.Apply(queue.Apply(queue.State{}, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}), pin)
	raw, err = queue.EncodeSnapshot(state)
	must(t, err)
	var snapshot map[string]any
	must(t, json.Unmarshal(raw, &snapshot))
	projection := snapshot["Authority"].(map[string]any)
	projection["complete"] = true
	projection["last_observed_at"] = pin.OpenMerge.Activation.SeeingReadAt
	projection["last_verified_at"] = pin.OpenMerge.Activation.SeeingReadAt
	raw, err = json.Marshal(snapshot)
	must(t, err)
	if _, err := queue.DecodeSnapshot(raw); err == nil || !strings.Contains(err.Error(), "replay journal and reverify authority") {
		t.Fatalf("test cursor not rejected for activation provenance: %v", err)
	}
	cursor, err := json.Marshal(outsidePushProgress{ScannedThrough: lastMove, State: raw})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `UPDATE observations SET value=$1 WHERE queue_id=$2 AND key=$3`, cursor, q, outsidePushCursorKey)
	must(t, err)

	first, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	competitor, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	if first.CursorResetReason == "" || first.progress.ScannedThrough >= lastMove || len(first.Moves) != 1 || first.Moves[0].NewSHA != "B" {
		t.Fatalf("rebuild did not reset to bounded journal prefix: %+v", first)
	}
	ok, err := s.CommitOutsidePushAudit(t.Context(), q, first, auditEvidence(first))
	must(t, err)
	if !ok {
		t.Fatal("rebuild lost its original cursor CAS")
	}
	ok, err = s.CommitOutsidePushAudit(t.Context(), q, competitor, auditEvidence(competitor))
	must(t, err)
	if ok {
		t.Fatal("competing rebuild overwrote newer progress")
	}
	restarted, err := Open(t.Context(), s.pool.Config().ConnString(), WithTestLockScope(s.testLockScope))
	must(t, err)
	defer restarted.Close()
	resumed := auditPass(t, restarted, q)
	if resumed.CursorResetReason != "" || len(resumed.Moves) != 2 || resumed.Moves[0].NewSHA != "C" || resumed.Moves[1].NewSHA != "D" {
		t.Fatalf("restart lost remaining transitions: %+v", resumed)
	}
	if len(auditPass(t, restarted, q).Moves) != 0 {
		t.Fatal("caught-up cursor repeated transitions")
	}
	loaded, _, err := restarted.Load(t.Context(), q)
	must(t, err)
	if moves := loaded.RecentOutsidePushes(); len(moves) != 3 || moves[0].NewSHA != "B" || moves[1].NewSHA != "C" || moves[2].NewSHA != "D" {
		t.Fatalf("rescan lost or duplicated durable receipts: %+v", moves)
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

func TestOutsidePushLandingAllowanceConsumedOnce(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.FastForward} {
		for _, next := range []string{"repair", "outside"} {
			t.Run(string(method)+"/"+next, func(t *testing.T) {
				s := newStore(t)
				q := ensureQueue(t, s, "")
				appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch}, queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch}, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: method, At: epoch}, queue.LandingStarted{BatchID: "b1", AttemptID: "a", FenceToken: "fence", At: epoch})
				landed := "tested"
				if method == queue.FastForward {
					landed = "head"
				}
				appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: landed, NewFence: "landed"})
				auditPass(t, s, q)
				appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
				a := auditPass(t, s, q)
				if len(a.Moves) != 1 || !a.Moves[0].PossibleStaleObservation {
					t.Fatalf("rewind not flagged: %+v", a.Moves)
				}
				if next == "outside" {
					appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
					a = auditPass(t, s, q)
					if len(a.Moves) != 1 {
						t.Fatalf("outside move lost: %+v", a.Moves)
					}
				}
				appendEvents(t, s, q, queue.BaseMoved{SHA: landed})
				a = auditPass(t, s, q)
				want := 0
				if next == "outside" {
					want = 1
				}
				if len(a.Moves) != want {
					t.Fatalf("repair/return classification: %+v", a.Moves)
				}
				// The allowance must remain consumed across separate durable cursor passes.
				appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
				a = auditPass(t, s, q)
				if len(a.Moves) != 1 || !a.Moves[0].PossibleStaleObservation {
					t.Fatalf("second rewind not flagged: %+v", a.Moves)
				}
				appendEvents(t, s, q, queue.BaseMoved{SHA: landed})
				a = auditPass(t, s, q)
				if len(a.Moves) != 1 || a.Moves[0].PreviousSHA != "base" || a.Moves[0].NewSHA != landed {
					t.Fatalf("attempt reused to hide second return: %+v", a.Moves)
				}
			})
		}
	}
}

func TestOutsidePushConsumedAllowanceSurvivesSettlementAtBuildBase(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"}, queue.BaseMoved{SHA: "base"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	a := auditPass(t, s, q)
	if len(a.Moves) != 1 || !a.Moves[0].PossibleStaleObservation {
		t.Fatalf("rewind lost: %+v", a.Moves)
	}
	appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"})
	a = auditPass(t, s, q)
	if len(a.Moves) != 1 || a.Moves[0].PreviousSHA != "base" {
		t.Fatalf("settlement hid explicit baseline/consumed allowance: %+v", a.Moves)
	}
}

func TestOutsidePushAbandonedAttemptInvalidatesPreviousAllowance(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "landed"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: epoch}, queue.BatchStarted{BatchID: "b2", BaseSHA: "tested", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head2"}}, At: epoch}, queue.TestRefBuilt{BatchID: "b2", TestedSHA: "tested2", Method: queue.Merge, At: epoch}, queue.LandingStarted{BatchID: "b2", AttemptID: "a2", FenceToken: "landed", At: epoch}, queue.BaseMoved{SHA: "outside"}, queue.BaseMoved{SHA: "base"}, queue.LandingAbandoned{BatchID: "b2", NewFence: "abandoned"})
	auditPass(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "tested"})
	a := auditPass(t, s, q)
	if len(a.Moves) != 1 {
		t.Fatalf("abandonment retained obsolete allowance: %+v", a.Moves)
	}
}
