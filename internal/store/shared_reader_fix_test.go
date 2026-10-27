package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
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

func traceAuditQueries(t *testing.T, s *Store) *auditQueryTrace {
	t.Helper()
	cfg := s.pool.Config()
	s.pool.Close()
	trace := &auditQueryTrace{}
	cfg.ConnConfig.Tracer = trace
	var err error
	s.pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	return trace
}

func TestSharedReaderLegacyCursorBoundedSuffixAndIdleResume(t *testing.T) {
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
	progress, err := json.Marshal(outsidePushProgress{ScannedThrough: last, State: raw})
	must(t, err)
	_, err = s.pool.Exec(t.Context(), `INSERT INTO observations(queue_id,key,value) VALUES($1,$2,$3)`, q, outsidePushCursorKey, progress)
	must(t, err)
	trace := traceAuditQueries(t, s)
	// An unmarked deployed cursor must resume immediately, even after restart.
	for pass := 0; pass < 3; pass++ {
		trace.reset()
		audit, err := s.ReadOutsidePushAudit(t.Context(), q)
		must(t, err)
		trace.assertSuffix(t, last, 0)
		if pass == 0 {
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
		if audit.CursorResetReason != "" || audit.progress.ScannedThrough != last || len(audit.Moves) != 0 {
			t.Fatalf("legacy cursor rebuilt or repeated historical moves: %+v", audit)
		}
		committed, err := s.CommitOutsidePushAudit(t.Context(), q, audit, nil)
		must(t, err)
		if !committed {
			t.Fatal("cursor CAS failed")
		}
		got, err := s.Observation(t.Context(), q, outsidePushCursorKey)
		must(t, err)
		if !bytes.Equal(got, progress) {
			t.Fatal("legacy envelope changed on idle commit")
		}
	}
	// Actual suffix pages remain bounded; no discarded prefix must be rebuilt.
	appendEvents(t, s, q, events...)
	through := last
	for _, want := range []int64{outsidePushScanLimit, outsidePushScanLimit, 7, 0} {
		trace.reset()
		audit, err := s.ReadOutsidePushAudit(t.Context(), q)
		must(t, err)
		trace.assertSuffix(t, through, want)
		if audit.CursorResetReason != "" {
			t.Fatal(audit.CursorResetReason)
		}
		committed, err := s.CommitOutsidePushAudit(t.Context(), q, audit, nil)
		must(t, err)
		if !committed {
			t.Fatal("cursor CAS failed")
		}
		through = audit.progress.ScannedThrough
	}
}

func TestSharedReaderLegacyCursorSideSuffixUpgradesAndResumes(t *testing.T) {
	for _, dependencyOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(dependencyOnly), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"})
			legacy := auditPass(t, s, q)
			h := readerObservation(t)
			h.Chunks, h.Commits = 2, h.Commits[:1]
			var evidence any = h
			if dependencyOnly {
				var token [32]byte
				token[0] = 1
				init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
				must(t, err)
				evidence = init
			}
			id := insertReaderEvidence(t, s, q, evidence)
			trace := traceAuditQueries(t, s)
			trace.reset()
			audit := auditPass(t, s, q)
			trace.assertSuffix(t, legacy.progress.ScannedThrough, 1)
			if audit.CursorResetReason != "" || audit.progress.ScannedThrough != id {
				t.Fatalf("legacy suffix did not fold: %+v", audit)
			}
			var header struct{ Version int }
			must(t, json.Unmarshal(audit.progress.State, &header))
			if header.Version != 9 {
				t.Fatal("side suffix did not persist v9")
			}
			// A new Store resumes entirely from durable v9 state, without a probe
			// or replay of the side prefix (also for dependency-only history).
			s = openTestStore(t, s.pool.Config().ConnString())
			trace = traceAuditQueries(t, s)
			trace.reset()
			again := auditPass(t, s, q)
			trace.assertSuffix(t, id, 0)
			if !bytes.Equal(audit.progress.State, again.progress.State) {
				t.Fatal("v9 projection changed on restart")
			}
			insertReaderEvidence(t, s, q, evidence)
			if _, err := s.ReadOutsidePushAudit(t.Context(), q); err == nil {
				t.Fatal("v9 resume discarded side projection: duplicate accepted")
			}
		})
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
