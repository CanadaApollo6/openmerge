package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func warningPin(t *testing.T, mode authority.Mode) queue.PolicyAuthorityApplied {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	e, err := queue.DecodeEvent(raw)
	must(t, err)
	pin := e.(queue.PolicyAuthorityApplied)
	pin.Authority = mode
	if mode == authority.GitHub {
		pin.OpenMerge = nil
	}
	return pin
}

func appendWarningPin(t *testing.T, s *Store, q QueueID, mode authority.Mode) int64 {
	t.Helper()
	pin := warningPin(t, mode)
	return appendEvents(t, s, q, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin)
}

func warnings(t *testing.T, s *Store, q QueueID) []OutsidePushWarning {
	t.Helper()
	var result []OutsidePushWarning
	must(t, s.VisitOutsidePushWarnings(t.Context(), q, func(w OutsidePushWarning) error { result = append(result, w); return nil }))
	return result
}

func restartWarningStore(t *testing.T, s *Store) *Store {
	t.Helper()
	next, err := Open(t.Context(), s.pool.Config().ConnString(), WithTestLockScope(s.testLockScope))
	must(t, err)
	t.Cleanup(next.Close)
	return next
}

func TestOutsidePushWarningDelayedAuditAuthorityBoundaries(t *testing.T) {
	for _, mode := range []authority.Mode{authority.OpenMerge, authority.GitHub} {
		for _, returnBeforeAudit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/return-before-audit-%t", mode, returnBeforeAudit), func(t *testing.T) {
				s := newStore(t)
				q := ensureQueue(t, s, "")
				initial := authority.GitHub
				if mode == authority.GitHub {
					initial = authority.OpenMerge
				}
				// This audit fixture starts with an already admitted attempt;
				// authority starts themselves require the atomic store gate.
				seedOutsideLanding(t, s, q)
				appendWarningPin(t, s, q, initial)
				before := appendEvents(t, s, q, queue.BaseMoved{SHA: "before-boundary"})
				// The pin itself immediately separates the two immutable BaseMoved rows.
				boundary := appendEvents(t, s, q, warningPin(t, mode))
				after := appendEvents(t, s, q, queue.BaseMoved{SHA: "after-boundary"})
				if boundary != before+1 || after != boundary+1 {
					t.Fatal("boundary not adjacent")
				}
				a := auditPass(t, s, q)
				if len(a.Moves) != 0 || len(a.progress.Pending) != 2 {
					t.Fatalf("audit escaped settlement hold: %+v", a)
				}
				// Delay past this boundary, optionally past a second mode change as
				// well. Each side must use its origin, never append position or time.
				if returnBeforeAudit {
					appendEvents(t, s, q, warningPin(t, initial))
				}
				s = restartWarningStore(t, s)
				a = auditPass(t, s, q)
				if len(a.Moves) != 0 {
					t.Fatal("restart lost the settlement hold")
				}
				appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "abandoned"})
				a, err := s.ReadOutsidePushAudit(t.Context(), q)
				must(t, err)
				if len(a.Moves) != 2 || a.Moves[0].ID != before || a.Moves[1].ID != after {
					t.Fatalf("bad audit origins: %+v", a.Moves)
				}
				// Enrichment remains intact, including truncated/unknown history caveats.
				enriched := auditEvidence(a)
				for i := range enriched {
					enriched[i].Commits = []string{}
				}
				enriched[0].HistoryKnown = true
				enriched[0].Truncated = true
				for i := range queue.OutsidePushCommitLimit {
					enriched[0].Commits = append(enriched[0].Commits, fmt.Sprintf("commit-%d", i))
				}
				enriched[0].Commits[0] = enriched[0].NewSHA
				ok, err := s.CommitOutsidePushAudit(t.Context(), q, a, enriched)
				must(t, err)
				if !ok {
					t.Fatal("audit commit lost")
				}
				expected := before
				if mode == authority.OpenMerge {
					expected = after
				}
				for range 2 {
					got := warnings(t, s, q)
					if len(got) != 1 || got[0].BaseMovedEventID != expected || got[0].VerificationBeforeLanding != "none" {
						t.Fatalf("eligibility used append-time authority: %+v", got)
					}
					records, err := s.EventsAfter(t.Context(), q, boundary)
					must(t, err)
					for _, r := range records {
						if push, ok := r.Event.(queue.OutsidePushRecorded); ok {
							var key string
							must(t, s.pool.QueryRow(t.Context(), `SELECT dedupe_key FROM events WHERE queue_id=$1 AND id=$2`, q, r.ID).Scan(&key))
							index := 0
							if push.NewSHA == "after-boundary" {
								index = 1
							}
							if key != fmt.Sprintf("outside-base/%d", a.Moves[index].ID) || !reflect.DeepEqual(push, enriched[index]) {
								t.Fatal("audit payload/association changed")
							}
						}
					}
					s = restartWarningStore(t, s)
					auditPass(t, s, q)
				}
			})
		}
	}
}

func TestOutsidePushAcknowledgementAtomicRetriesAndIsolation(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	other := ensureQueue(t, s, "other")
	appendWarningPin(t, s, q, authority.OpenMerge)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	move := appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
	auditPass(t, s, q)
	source := warnings(t, s, q)[0].EventID
	before, _, err := s.Load(t.Context(), q)
	must(t, err)
	snap, err := queue.EncodeSnapshot(before)
	must(t, err)
	watermark, err := s.Watermark(t.Context(), q)
	must(t, err)
	for _, tc := range []struct {
		q     QueueID
		event int64
		sha   string
	}{{q, source, "wrong"}, {q, move, "outside"}, {q, source + 100, "outside"}, {other, source, "outside"}} {
		r, err := s.AcknowledgeOutsidePush(t.Context(), tc.q, tc.event, tc.sha, "operator")
		if err == nil || r != nil {
			t.Fatalf("accepted wrong source/SHA/queue: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := s.AcknowledgeOutsidePush(ctx, q, source, "outside", "cancelled")
	if err == nil || r != nil {
		t.Fatal("cancelled command returned receipt")
	}
	// Failure after insertion must roll back the transaction and return no receipt.
	_, err = s.pool.Exec(t.Context(), `CREATE FUNCTION reject_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='outside_push_acknowledged' THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$;
 CREATE CONSTRAINT TRIGGER reject_ack AFTER INSERT ON events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_ack()`)
	must(t, err)
	r, err = s.AcknowledgeOutsidePush(t.Context(), q, source, "outside", "failed")
	if err == nil || r != nil {
		t.Fatal("failed commit returned receipt")
	}
	_, err = s.pool.Exec(t.Context(), `DROP TRIGGER reject_ack ON events`)
	must(t, err)
	gotWatermark, err := s.Watermark(t.Context(), q)
	must(t, err)
	if gotWatermark != watermark || len(warnings(t, s, q)) != 1 {
		t.Fatal("failure changed durable evidence")
	}

	// Cancellation while waiting for the queue lock must not append a receipt.
	locked, err := s.pool.Begin(t.Context())
	must(t, err)
	must(t, s.lockQueue(t.Context(), locked, q))
	waiting, cancelWait := context.WithTimeout(t.Context(), 20*time.Millisecond)
	r, err = s.AcknowledgeOutsidePush(waiting, q, source, "outside", "timed out")
	cancelWait()
	if err == nil || r != nil {
		t.Fatal("lock wait cancellation returned receipt")
	}
	must(t, locked.Rollback(t.Context()))
	var wg sync.WaitGroup
	receipts := make(chan *OutsidePushAcknowledgement, 12)
	for i := range 12 {
		wg.Go(func() {
			r, err := s.AcknowledgeOutsidePush(t.Context(), q, source, "outside", fmt.Sprintf("operator-%d", i))
			if err != nil {
				t.Error(err)
			}
			receipts <- r
		})
	}
	wg.Wait()
	close(receipts)
	var original *OutsidePushAcknowledgement
	for r := range receipts {
		if r == nil {
			t.Fatal("missing receipt")
		}
		if original == nil {
			original = r
		}
		if !reflect.DeepEqual(original, r) {
			t.Fatal("retry returned new timestamp/operator/event")
		}
	}
	s = restartWarningStore(t, s)
	retry, err := s.AcknowledgeOutsidePush(t.Context(), q, source, "outside", "different operator")
	must(t, err)
	if !reflect.DeepEqual(original, retry) || len(warnings(t, s, q)) != 0 || len(warnings(t, s, other)) != 0 {
		t.Fatal("restart/idempotency/isolation failure")
	}
	if r, err := s.AcknowledgeOutsidePush(t.Context(), q, source, "wrong", "retry"); err == nil || r != nil {
		t.Fatal("receipt bypassed SHA validation")
	}
	if count(t, s, `SELECT count(*) FROM events WHERE type='outside_push_acknowledged'`) != 1 {
		t.Fatal("duplicate acknowledgement")
	}
	after, _, err := s.Load(t.Context(), q)
	must(t, err)
	next, err := queue.EncodeSnapshot(after)
	must(t, err)
	if !bytes.Equal(snap, next) {
		t.Fatal("acknowledgement changed queue state")
	}
}

func TestOutsidePushWarningsUncappedArchivedAndLegacy(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	legacy := ensureQueue(t, s, "legacy")
	appendWarningPin(t, s, q, authority.OpenMerge)
	for _, id := range []QueueID{q, legacy} {
		appendEvents(t, s, id, queue.BaseMoved{SHA: "base"})
		for i := range 27 {
			appendEvents(t, s, id, queue.BaseMoved{SHA: fmt.Sprintf("outside-%d", i)})
		}
		auditPass(t, s, id)
	}
	appendWarningPin(t, s, q, authority.GitHub)
	appendEvents(t, s, q, queue.LifecycleChanged{State: "retired", Detail: "retired"})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.RecentOutsidePushes()) != queue.OutsidePushLimit || len(warnings(t, s, q)) != 27 {
		t.Fatal("warnings capped at recent projection, rollback or retirement")
	}
	archived := warnings(t, s, q)[0]
	_, err = s.AcknowledgeOutsidePush(t.Context(), q, archived.EventID, archived.NewSHA, "retired queue operator")
	must(t, err)
	if len(warnings(t, s, q)) != 26 {
		t.Fatal("retired acknowledgement failed")
	}
	before, err := s.Watermark(t.Context(), legacy)
	must(t, err)
	if len(warnings(t, s, legacy)) != 0 {
		t.Fatal("legacy push relabelled")
	}
	records, err := s.EventsAfter(t.Context(), legacy, 0)
	must(t, err)
	var source int64
	for _, r := range records {
		if _, ok := r.Event.(queue.OutsidePushRecorded); ok {
			source = r.ID
			break
		}
	}
	// Explicit github-mode acknowledgement is permitted, and never creates an
	// authority warning. It still has the same first-emission binary boundary.
	receipt, err := s.AcknowledgeOutsidePush(t.Context(), legacy, source, "outside-0", "operator")
	must(t, err)
	if receipt.SourceEventID != source || len(warnings(t, s, legacy)) != 0 {
		t.Fatal("github acknowledgement semantics changed")
	}
	if count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND id>$2`, legacy, before) != 1 {
		t.Fatal("unexpected legacy writes")
	}
	// Missing queue, cancellation, and visitor failure cannot look like success.
	if err = s.VisitOutsidePushWarnings(t.Context(), 999999, func(OutsidePushWarning) error { return nil }); err == nil {
		t.Fatal("unknown queue accepted")
	}
	sentinel := errors.New("stop visitor")
	if err = s.VisitOutsidePushWarnings(t.Context(), q, func(OutsidePushWarning) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = s.VisitOutsidePushWarnings(ctx, q, func(OutsidePushWarning) error { return nil }); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}

func TestOutsidePushWarningPagePlansAndGrowth(t *testing.T) {
	for _, n := range []int{5000, 50000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendWarningPin(t, s, q, authority.OpenMerge)
			_, payload, err := queue.EncodeEvent(queue.RefreshRequested{})
			must(t, err)
			rows := make([][]any, n)
			for i := range rows {
				rows[i] = []any{q, "refresh_requested", string(payload)}
			}
			_, err = s.pool.CopyFrom(t.Context(), pgx.Identifier{"events"}, []string{"queue_id", "type", "payload"}, pgx.CopyFromRows(rows))
			must(t, err)
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
			for i := range 30 {
				appendEvents(t, s, q, queue.BaseMoved{SHA: fmt.Sprintf("outside-%d", i)})
			}
			for i := 0; i < n/warningPageSize+3; i++ {
				auditPass(t, s, q)
			}
			_, err = s.pool.Exec(t.Context(), `ANALYZE events`)
			must(t, err)
			start := time.Now()
			got := warnings(t, s, q)
			t.Logf("rows=%d warnings=%d bounded-page scan=%s", n, len(got), time.Since(start))
			if len(got) != 30 {
				t.Fatal("lost warnings across pages")
			}
			// The bounded page exposes dedupe_key, unlike the existing log reader.
			tx, err := s.pool.Begin(t.Context())
			must(t, err)
			page, err := eventPage(t.Context(), tx, q, got[0].EventID-1)
			must(t, err)
			if page[0].DedupeKey == nil || *page[0].DedupeKey != fmt.Sprintf("outside-base/%d", got[0].BaseMovedEventID) {
				t.Fatal("page lost origin association")
			}
			must(t, tx.Rollback(t.Context()))
			for _, probe := range []struct {
				sql  string
				args []any
			}{
				{warningPageSQL, []any{q, 0, warningPageSize}},
				{`SELECT id,payload,dedupe_key FROM events WHERE queue_id=$1 AND dedupe_key=$2`, []any{q, fmt.Sprintf("outside-base/%d", got[0].BaseMovedEventID)}},
			} {
				plan, err := s.pool.Query(t.Context(), "EXPLAIN (ANALYZE, BUFFERS) "+probe.sql, probe.args...)
				must(t, err)
				lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
				must(t, err)
				t.Logf("query=%s plan=%v", probe.sql, lines)
			}
			started := time.Now()
			_, err = s.AcknowledgeOutsidePush(t.Context(), q, got[0].EventID, got[0].NewSHA, "timing")
			must(t, err)
			t.Logf("rows=%d acknowledge=%s", n, time.Since(started))
		})
	}
}

func TestOutsidePushWarningsSnapshotAndStalePin(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	pin := warningPin(t, authority.OpenMerge)
	// A pin without its governing ConfigApplied is ignored, exactly as in replay.
	appendEvents(t, s, q, pin, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "github-move"})
	auditPass(t, s, q)
	if len(warnings(t, s, q)) != 0 {
		t.Fatal("stale pin became authoritative")
	}
	appendWarningPin(t, s, q, authority.OpenMerge)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "authority-move"})
	a, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	evidence := auditEvidence(a)
	evidence[0].HistoryKnown = true
	evidence[0].Commits = []string{"authority-move"}
	ok, err := s.CommitOutsidePushAudit(t.Context(), q, a, evidence)
	must(t, err)
	if !ok {
		t.Fatal("audit commit failed")
	}
	got := warnings(t, s, q)
	calls := 0
	must(t, s.VisitOutsidePushWarnings(t.Context(), q, func(w OutsidePushWarning) error {
		calls++
		if !reflect.DeepEqual(w, got[0]) {
			t.Fatal("read changed before acknowledgement")
		}
		_, err := s.AcknowledgeOutsidePush(t.Context(), q, w.EventID, w.NewSHA, "concurrent operator")
		w.Commits[0] = "caller mutation"
		return err
	}))
	if calls != 1 || len(warnings(t, s, q)) != 0 {
		t.Fatal("inconsistent snapshot or receipt")
	}
	records, err := s.EventsAfter(t.Context(), q, got[0].EventID-1)
	must(t, err)
	push := records[0].Event.(queue.OutsidePushRecorded)
	if push.Commits[0] != "authority-move" {
		t.Fatal("caller mutation reached journal")
	}
}

func TestOutsidePushLedgerOrphansRemainAnomaliesAfterAcknowledgement(t *testing.T) {
	for _, key := range []string{"NULL", "", "outside-base/999999999", "outside-base/1", "garbage"} {
		t.Run(key, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendWarningPin(t, s, q, authority.OpenMerge)
			bad := QueueKey{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "main"}
			sibling := QueueKey{Installation: "installation/1", Repository: "owner/repo", BaseBranch: "release"}
			siblingID, err := s.EnsureQueue(t.Context(), sibling)
			must(t, err)
			appendWarningPin(t, s, siblingID, authority.OpenMerge)
			_, err = s.Journals(t.Context(), []QueueKey{bad, sibling})
			must(t, err)
			// Deliberately inject an audit without a valid originating BaseMoved.
			push := queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "orphan", VerificationBeforeLanding: "none", At: time.Now().UTC()}
			name, raw, err := queue.EncodeEvent(push)
			must(t, err)
			var id int64
			var dedupe any
			if key != "NULL" {
				dedupe = key
			}
			must(t, s.pool.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,dedupe_key) VALUES($1,$2,$3::jsonb,$4) RETURNING id`, q, name, string(raw), dedupe).Scan(&id))
			for i := 0; i < 2; i++ {
				err = s.VisitOutsidePushWarnings(t.Context(), q, func(OutsidePushWarning) error { t.Fatal("orphan presented as valid warning"); return nil })
				if err == nil || !strings.Contains(err.Error(), "ledger anomaly: orphan") {
					t.Fatalf("silently dropped orphan: %v", err)
				}
				summary, readErr := s.ReadOutsidePushWarnings(t.Context(), q)
				if readErr == nil || summary.Error != "ledger_unavailable" || summary.Outstanding != 0 {
					t.Fatalf("background hid orphan: %+v %v", summary, readErr)
				}
				js, journalErr := s.Journals(t.Context(), []QueueKey{bad, sibling})
				must(t, journalErr)
				if len(js) != 2 || js[0].OutsideWarnings.Error != "ledger_unavailable" || js[0].OutsideWarnings.Outstanding != 0 || js[1].OutsideWarnings.Error != "" {
					t.Fatalf("orphan not queue-local: %+v", js)
				}
				if key == "NULL" && js[0].Events[len(js[0].Events)-1-i].DedupeKey != nil {
					t.Fatal("journal replaced NULL key")
				}
				if i == 0 {
					_, err = s.AcknowledgeOutsidePush(t.Context(), q, id, push.NewSHA, "operator")
					must(t, err)
				}
			}
		})
	}
}

func TestEventRecordDedupeLoadingIsExplicit(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
	records, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	if len(records) != 1 || records[0].DedupeKey != nil {
		t.Fatal("EventsAfter pretended to load dedupe association")
	}
	name, raw, err := queue.EncodeEvent(queue.RefreshRequested{})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,dedupe_key) VALUES($1,$2,$3::jsonb,'')`, q, name, string(raw))
	must(t, err)
	tx, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	page, err := eventPage(t.Context(), tx, q, 0)
	must(t, err)
	if len(page) != 2 || page[0].DedupeKey != nil || page[1].DedupeKey == nil || *page[1].DedupeKey != "" {
		t.Fatal("loaded SQL NULL association was lost")
	}
}

func TestOutsidePushLedgerMalformedAssociationsAgreeInEveryMode(t *testing.T) {
	for _, mode := range []authority.Mode{authority.GitHub, authority.OpenMerge} {
		for _, kind := range []string{"audit-type", "ack-type", "ack-sha"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				s := newStore(t)
				key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}
				q, err := s.EnsureQueue(t.Context(), key)
				must(t, err)
				appendWarningPin(t, s, q, mode)
				origin := appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
				insert := func(event queue.Event, key string) int64 {
					name, raw, err := queue.EncodeEvent(event)
					must(t, err)
					var id int64
					must(t, s.pool.QueryRow(t.Context(), `INSERT INTO events(queue_id,type,payload,dedupe_key) VALUES($1,$2,$3::jsonb,$4) RETURNING id`, q, name, string(raw), key).Scan(&id))
					return id
				}
				// Warm both caches before injecting the malformed suffix.
				_, err = s.ReadOutsidePushWarnings(t.Context(), q)
				must(t, err)
				_, err = s.Journals(t.Context(), []QueueKey{key})
				must(t, err)
				if kind == "audit-type" {
					insert(queue.RefreshRequested{}, fmt.Sprintf("outside-base/%d", origin))
				} else {
					audit := insert(queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "outside", VerificationBeforeLanding: "none", At: time.Now().UTC()}, fmt.Sprintf("outside-base/%d", origin))
					var event queue.Event = queue.RefreshRequested{}
					if kind == "ack-sha" {
						event = queue.OutsidePushAcknowledged{SourceEventID: audit, SHA: "wrong", Operator: "operator", At: time.Now().UTC()}
					}
					insert(event, acknowledgementKey(audit))
				}
				for _, db := range []*Store{s, restartWarningStore(t, s)} {
					if err := db.VisitOutsidePushWarnings(t.Context(), q, func(OutsidePushWarning) error { return nil }); err == nil || !strings.Contains(err.Error(), "ledger anomaly") {
						t.Fatalf("scan hid malformed row: %v", err)
					}
					summary, err := db.ReadOutsidePushWarnings(t.Context(), q)
					if err == nil || summary.Error != "ledger_unavailable" {
						t.Fatalf("cache hid malformed row: %+v %v", summary, err)
					}
					js, err := db.Journals(t.Context(), []QueueKey{key})
					must(t, err)
					if js[0].OutsideWarnings.Error != "ledger_unavailable" {
						t.Fatalf("fold hid malformed row: %+v", js[0].OutsideWarnings)
					}
				}
			})
		}
	}
}
