package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// Use the existing authorized landing fixture, with companion source evidence
// bound to that exact revision. The decision itself is inserted only by start.
func landingSide(t *testing.T, c landingCase) func([]uint64) (SideAppend, error) {
	t.Helper()
	h := sideInventory(t)
	h.Request.ChangeID = string(c.Fresh.Basis.ChangeID)
	h.Request.Revision = strconv.FormatUint(uint64(c.Fresh.Basis.Revision), 10)
	h.Request.Head = c.Fresh.Basis.Head
	h.Digest = pj.InventoryDigest(h, h.Commits)
	bundle := sideBundle(t, h, c.Start.AttemptID, "atomic-companion", nil)
	return func(decisions []uint64) (SideAppend, error) {
		if len(decisions) != 1 {
			return SideAppend{}, errors.New("missing reserved decision")
		}
		return SideAppend{Count: 2, DedupeKey: bundle.DedupeKey, Build: func(ids []uint64) ([]journalreader.Event, error) {
			events, err := bundle.Build([]uint64{ids[0], decisions[0], ids[1], ids[1] + 1})
			if err != nil {
				return nil, err
			}
			l := events[2].Provenance.(pj.Landing)
			l.BatchID = c.Start.BatchID
			l.Members[0].Mapping.Build.Result = c.Land.TestedSHA
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			return []journalreader.Event{events[0], {Provenance: l}}, nil
		}}, nil
	}
}

func startWithSide(ctx context.Context, s *Store, claim ClaimedAction, c landingCase, d queue.LandingDecisionRecorded, build func([]uint64) (SideAppend, error)) error {
	return s.startLandingWithSide(ctx, claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, s.authorizeStart, build)
}

func TestLandingDecisionCompanionSharedTransaction(t *testing.T) {
	for _, failure := range []string{"build", "validation", "insert", "none"} {
		t.Run(failure, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			auditPass(t, s, q)
			beforeCursor, err := s.Observation(t.Context(), q, outsidePushCursorKey)
			must(t, err)
			before := eventCount(t, s, q)
			build := landingSide(t, c)
			if failure == "build" {
				build = func(ids []uint64) (SideAppend, error) {
					// The decision really exists inside THIS transaction before failure.
					if len(ids) != 1 || ids[0] == 0 {
						t.Error("decision ID missing")
					}
					return SideAppend{}, errors.New("forced failure after decision insert")
				}
			}
			if failure == "validation" {
				inner := build
				build = func(ids []uint64) (SideAppend, error) {
					request, err := inner(ids)
					request.Build = func([]uint64) ([]journalreader.Event, error) {
						return nil, errors.New("forced companion validation failure")
					}
					return request, err
				}
			}
			if failure == "insert" {
				_, err := s.pool.Exec(t.Context(), `CREATE FUNCTION reject_companion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='landing_provenance_recorded' THEN RAISE EXCEPTION 'forced companion insert failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_companion BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_companion()`)
				must(t, err)
			}
			read, readErr := s.BeginApprovalRead(t.Context(), q, []queue.ChangeID{c.Fresh.Basis.ChangeID})
			must(t, readErr)
			err = startWithSide(read.Context(t.Context()), s, claim, c, d, build)
			if failure != "none" {
				if err == nil || !strings.Contains(err.Error(), "forced") {
					t.Fatal("forced failure ignored", err)
				}
				assertNoLandingSideEffects(t, s, q, claim, before)
				after, e := s.Observation(t.Context(), q, outsidePushCursorKey)
				must(t, e)
				if !bytes.Equal(beforeCursor, after) {
					t.Fatal("cursor upgrade escaped rollback")
				}
				if count(t, s, `SELECT count(*) FROM observations WHERE queue_id=$1 AND key LIKE 'side-append/%'`, q) != 0 {
					t.Fatal("checkpoint/manifest escaped rollback")
				}
				return
			}
			must(t, err)
			if eventCount(t, s, q) != before+5 {
				t.Fatal("decision/start/companion bundle incomplete")
			}
			if count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded'`, q) != 1 {
				t.Fatal("missing companion")
			}
			_, version := cursorVersion(t, s, q)
			if version != 9 {
				t.Fatal("missing transactional cursor upgrade")
			}
			state, _, err := s.Load(t.Context(), q)
			must(t, err)
			if len(state.LandingDecisions()) != 1 || state.Active == nil || state.Active.Phase != queue.Landing {
				t.Fatal("missing atomic landing state")
			}
		})
	}
}

// Synchronize both lock orders and include a queues FOR UPDATE holder. An events
// INSERT takes FK KEY SHARE on this row. Taking FOR UPDATE in either writer
// before the queue advisory would close a cycle with the other writer's FK.
func TestLandingSideAppendLockOrder(t *testing.T) {
	for _, first := range []string{"start", "append"} {
		t.Run(first, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			other := openTestStore(t, s.pool.Config().ConnString())
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			row, err := s.pool.Begin(ctx)
			must(t, err)
			defer func() { _ = row.Rollback(context.Background()) }()
			_, err = row.Exec(ctx, `SELECT id FROM queues WHERE id=$1 FOR UPDATE`, q)
			must(t, err)
			var holderPID int
			must(t, row.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))
			entered, release, contending := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			appendRequest := sideObservation(sideInventory(t), "concurrent-inventory")
			// Pause the first writer AFTER it owns the advisory, before FK inserts.
			if first == "append" {
				inner := appendRequest.Build
				appendRequest.Build = func(ids []uint64) ([]journalreader.Event, error) { close(entered); <-release; return inner(ids) }
			}
			other.beforeQueueLock = func() { close(contending) }
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			start := func(store *Store, paused bool) error {
				if !paused {
					return startWithSide(ctx, store, claim, c, d, landingSide(t, c))
				}
				calls := 0
				authorize := func(ctx context.Context, tx pgx.Tx, e emission, event queue.Event) (bool, error) {
					stale, err := store.authorizeStart(ctx, tx, e, event)
					calls++
					if calls == 2 && err == nil {
						close(entered)
						<-release
					}
					return stale, err
				}
				return store.startLandingWithSide(ctx, claim, c.Config, c.Policy, []queue.ApprovalEligibilityObserved{c.Fresh}, []queue.LandingDecisionRecorded{d}, c.Start, authorize, landingSide(t, c))
			}
			if first == "start" {
				go func() { firstDone <- start(s, true) }()
			} else {
				go func() { _, e := s.AppendSide(ctx, q, appendRequest); firstDone <- e }()
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if first == "start" {
				go func() { _, e := other.AppendSide(ctx, q, appendRequest); secondDone <- e }()
			} else {
				go func() { secondDone <- start(other, false) }()
			}
			select {
			case <-contending:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Verify the second transaction is actually waiting on the advisory,
			// rather than relying on goroutine scheduling or a fixed sleep.
			for {
				var waiting bool
				must(t, s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=1330464081 AND objid=hashtext($1 || '/' || $2)::oid AND NOT granted)`, s.testLockScope, fmt.Sprint(q)).Scan(&waiting))
				if waiting {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
			}
			// Let the first writer reach a real FK KEY SHARE wait while the
			// second still waits on its queue advisory. The row holder never
			// needs either writer's locks, so releasing it must drain both.
			close(release)
			for {
				var blocked bool
				must(t, s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, holderPID).Scan(&blocked))
				if blocked {
					break
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
			}
			must(t, row.Commit(ctx))
			must(t, <-firstDone)
			must(t, <-secondDone)
		})
	}
}

func TestRecoverLandedCompanionAfterCrash(t *testing.T) {
	s := newStore(t)
	q, claim, c, d := decisionLand(t, s)
	must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
	staleID := appendEvents(t, s, q, queue.Landed{BatchID: "stale-batch", NewBaseSHA: d.LandedSHA, NewFence: "ignored"})
	landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
	// Reopen the store to exercise a restart with the persisted append checkpoint.
	restarted := openTestStore(t, s.pool.Config().ConnString())
	var raw []byte
	must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded'`, q).Scan(&raw))
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	landed := e.(pj.Landing)
	landed.Stage = "landed"
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].Mapping.Build.ID = "recovered-landed-composition"
	landed.Members[0].Mapping.Build.Result = d.LandedSHA
	landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
	request := SideAppend{Count: 1, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Provenance: landed}}, nil }}
	before := eventCount(t, s, q)
	if _, err := restarted.RecoverLanded(t.Context(), q, staleID, request); err == nil {
		t.Fatal("stale/no-op Landed accepted as recovery evidence")
	}
	// Landed must not have retired the tested evidence prematurely.
	reader, _, err := replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
	must(t, err)
	if len(reader.Provenance.Records()) != 2 {
		t.Fatal("crash lost tested evidence")
	}
	id, err := restarted.RecoverLanded(t.Context(), q, landedID, request)
	must(t, err)
	if id <= landedID || eventCount(t, s, q) != before+1 {
		t.Fatal("recovery did not append once")
	}
	reader, _, err = replayJournal(t.Context(), s.pool, q, queue.State{BaseBranch: "release"}, 0, true)
	must(t, err)
	if len(reader.Provenance.Records()) != 0 {
		t.Fatal("recovery did not retire completed evidence")
	}
	checkpoint, err := s.Observation(t.Context(), q, sideCheckpointKey)
	must(t, err)
	for range 3 {
		got, e := restarted.RecoverLanded(t.Context(), q, landedID, request)
		must(t, e)
		if got != id {
			t.Fatal("retry changed watermark")
		}
	}
	after, err := s.Observation(t.Context(), q, sideCheckpointKey)
	must(t, err)
	if eventCount(t, s, q) != before+1 || !bytes.Equal(checkpoint, after) {
		t.Fatal("retry mutated durable state")
	}
	// A changed bundle must not be silently accepted by the deterministic key.
	landed.Members[0].Mapping.Build.ID = "different-composition"
	landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
	if _, err := restarted.RecoverLanded(t.Context(), q, landedID, request); err == nil {
		t.Fatal("canonical mismatch accepted")
	}
	if _, err := restarted.RecoverLanded(t.Context(), q, landedID-1, request); err == nil {
		t.Fatal("non-Landed evidence accepted")
	}
}
