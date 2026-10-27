package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type auditQueryTrace struct {
	mu      sync.Mutex
	queries []pgx.TraceQueryStartData
	rows    int64
}

func (t *auditQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.Contains(data.SQL, "FROM events") {
		t.queries = append(t.queries, data)
		return context.WithValue(ctx, auditTraceKey{}, true)
	}
	return ctx
}

type auditTraceKey struct{}

func (t *auditQueryTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(auditTraceKey{}) == true {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.rows += data.CommandTag.RowsAffected()
	}
}
func (t *auditQueryTrace) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries, t.rows = nil, 0
}
func (t *auditQueryTrace) assertSuffix(tst *testing.T, after int64, wantRows int64) {
	tst.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.queries) != 1 || t.rows != wantRows {
		tst.Fatalf("audit queried history: %d queries, %d rows; want one suffix with %d rows: %+v", len(t.queries), t.rows, wantRows, t.queries)
	}
	query := t.queries[0]
	if !strings.Contains(query.SQL, "id > $2 ORDER BY id LIMIT $3") || fmt.Sprint(query.Args[1]) != fmt.Sprint(after) || query.Args[2] != outsidePushScanLimit {
		tst.Fatalf("unbounded audit query: %+v", query)
	}
}

func TestSharedReaderLegacyCursorBoundedRebuildAndIdleResume(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	events := make([]queue.Event, 2*outsidePushScanLimit+7)
	for i := range events {
		events[i] = queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true}
	}
	last := appendEvents(t, s, q, events...)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	raw, err := queue.EncodeSnapshot(state)
	must(t, err)
	// An unmarked cursor might have been rewritten by a rolled-back binary.
	progress, err := json.Marshal(outsidePushProgress{ScannedThrough: last, State: raw})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, progress)
	must(t, err)
	cfg := s.pool.Config()
	s.pool.Close()
	trace := &auditQueryTrace{}
	cfg.ConnConfig.Tracer = trace
	s.pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	var through int64
	for pass := 0; pass < 4; pass++ {
		trace.reset()
		audit, err := s.ReadOutsidePushAudit(t.Context(), q)
		must(t, err)
		want := int64(outsidePushScanLimit)
		if pass == 2 {
			want = 7
		}
		if pass == 3 {
			want = 0
		}
		trace.assertSuffix(t, through, want)
		if pass == 3 {
			// EXPLAIN uses the actual traced suffix SQL/arguments against this
			// isolated schema. Pin scanned buffers as well as returned rows.
			var plan []struct {
				Plan struct {
					Hit  int `json:"Shared Hit Blocks"`
					Read int `json:"Shared Read Blocks"`
				} `json:"Plan"`
			}
			var raw []byte
			query := trace.queries[0]
			must(t, s.pool.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.SQL, query.Args...).Scan(&raw))
			must(t, json.Unmarshal(raw, &plan))
			if len(plan) != 1 || plan[0].Plan.Hit+plan[0].Plan.Read > 16 {
				t.Fatalf("idle cursor scanned historical buffers: %s", raw)
			}
		}
		if (pass == 0) != (audit.CursorResetReason != "") {
			t.Fatalf("unexpected reset: %+v", audit)
		}
		if audit.progress.ReaderVersion != 1 || audit.progress.HasSideEvidence {
			t.Fatal("legacy prefix certification lost")
		}
		committed, err := s.CommitOutsidePushAudit(t.Context(), q, audit, nil)
		must(t, err)
		if !committed {
			t.Fatal("cursor CAS failed")
		}
		through = audit.progress.ScannedThrough
	}
	if through != last {
		t.Fatal("rebuild did not reach watermark")
	}
}

func TestSharedReaderRollbackCursorRevalidatesSidePrefix(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	h := readerObservation(t)
	h.Chunks, h.Commits = 2, h.Commits[:1]
	id := insertReaderEvidence(t, s, q, h)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	raw, err := queue.EncodeSnapshot(state)
	must(t, err)
	// Model an older shared reader that discarded the side projection/marker.
	progress, err := json.Marshal(outsidePushProgress{ScannedThrough: id, State: raw})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, progress)
	must(t, err)
	audit, err := s.ReadOutsidePushAudit(t.Context(), q)
	must(t, err)
	if audit.CursorResetReason == "" || !audit.progress.HasSideEvidence {
		t.Fatal("rollback cursor hid side prefix")
	}
	committed, err := s.CommitOutsidePushAudit(t.Context(), q, audit, nil)
	must(t, err)
	if !committed {
		t.Fatal("cursor did not commit")
	}
	// A duplicate chunk zero must fail after the cursor cut, rather than being
	// accepted into an empty side projection on roll-forward.
	insertReaderEvidence(t, s, q, h)
	if _, err := s.ReadOutsidePushAudit(t.Context(), q); err == nil {
		t.Fatal("side prefix was not reconstructed")
	}
}

func TestSharedReaderOutsidePushLedgerAcceptsSideEvidence(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendWarningPin(t, s, q, authority.OpenMerge)
	insertReaderEvidence(t, s, q, readerObservation(t))
	origin := appendEvents(t, s, q, queue.BaseMoved{SHA: "outside"})
	event := queue.OutsidePushRecorded{PreviousSHA: "before", NewSHA: "outside", At: epoch, VerificationBeforeLanding: "none"}
	receipt, err := s.Append(t.Context(), q, []queue.Event{event}, fmt.Sprintf("outside-base/%d", origin))
	must(t, err)
	got := warnings(t, s, q) // exercises eventPage and exact dedupe lookups
	if len(got) != 1 || got[0].EventID != receipt || got[0].BaseMovedEventID != origin {
		t.Fatalf("lost warning across side evidence: %+v", got)
	}
	_, err = s.AcknowledgeOutsidePush(t.Context(), q, receipt, "outside", "operator")
	must(t, err)
	if got := warnings(t, s, q); len(got) != 0 {
		t.Fatalf("acknowledgement lost: %+v", got)
	}
}
