package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func traceWarningStore(t *testing.T, s *Store) *landingSQLTrace {
	t.Helper()
	original := s.pool
	trace := &landingSQLTrace{}
	cfg := original.Config().Copy()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	s.pool = traced
	t.Cleanup(original.Close)
	return trace
}
func traceReset(trace *landingSQLTrace) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.statements = nil
}
func traceStatements(trace *landingSQLTrace) []string {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]string{}, trace.statements...)
}

func TestJournalWarningQueryCountsAndIncrementalWatermark(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}
	// ensureQueue uses release; read its actual partition.
	must(t, s.pool.QueryRow(t.Context(), `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&key.Installation, &key.Repository, &key.BaseBranch, &key.Lane))
	events := make([]queue.Event, 1024)
	for i := range events {
		events[i] = queue.RefreshRequested{}
	}
	appendEvents(t, s, q, events...)
	trace := traceWarningStore(t, s)
	read := func(label string) Journal {
		traceReset(trace)
		js, err := s.Journals(t.Context(), []QueueKey{key})
		must(t, err)
		sql := traceStatements(trace)
		if len(sql) != 4 {
			t.Fatalf("%s: expected begin + queue + events + commit; got %v", label, sql)
		}
		for _, stmt := range sql {
			if strings.Contains(stmt, "LIMIT") || strings.Contains(stmt, "dedupe_key=$2") {
				t.Fatalf("%s: duplicate ledger query: %s", label, stmt)
			}
		}
		t.Logf("%s: total queries=%d; extra ledger queries=0", label, len(sql))
		return js[0]
	}
	if read("legacy 1024 events").HasAuthorityPin {
		t.Fatal("legacy entered ledger")
	}
	if _, loaded := s.warningCaches.Load(q); loaded {
		t.Fatal("legacy allocated a ledger cache")
	}
	appendWarningPin(t, s, q, authority.OpenMerge)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "outside"})
	for range 6 {
		auditPass(t, s, q)
	}
	j := read("authority cold")
	if j.OutsideWarnings.Outstanding != 1 {
		t.Fatal(j.OutsideWarnings)
	}
	cache := s.warningCache(q)
	before := cache.through
	read("authority unchanged")
	if cache.through != before {
		t.Fatal("unchanged watermark advanced")
	}
	// Already folded origins never get revisited. Poisoning the cached policy
	// boundaries would lose the old warning if a request rebuilt the prefix.
	modeCount := len(cache.modes)
	appendEvents(t, s, q, queue.RefreshRequested{})
	read("authority one new event")
	if cache.through <= before || len(cache.modes) != modeCount {
		t.Fatal("incremental fold rebuilt the prefix")
	}
	// Background refresh reads one suffix page; probes scale with NEW audits.
	traceReset(trace)
	_, err := s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if got := len(traceStatements(trace)); got != 3 {
		t.Fatalf("unchanged background queries=%d", got)
	}
	appendEvents(t, s, q, queue.BaseMoved{SHA: "new-1"}, queue.BaseMoved{SHA: "new-2"})
	auditPass(t, s, q)
	traceReset(trace)
	summary, err := s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if got := len(traceStatements(trace)); got != 5 {
		t.Fatalf("two new audits: queries=%d, want 3 + 2 origin probes: %v", got, traceStatements(trace))
	}
	if summary.Outstanding != 3 {
		t.Fatal(summary)
	}
	t.Log("background: unchanged=3 queries; 2 new audits=5 queries (2 indexed origin probes)")
}

func TestJournalWarningErrorIsolatesQueueAndReporting(t *testing.T) {
	s := newStore(t)
	var keys []QueueKey
	for i := range 2 {
		key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: fmt.Sprintf("base-%d", i)}
		q, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		appendWarningPin(t, s, q, authority.OpenMerge)
		if i == 0 {
			appendEvents(t, s, q, queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "orphan", At: time.Now(), VerificationBeforeLanding: "none"})
		}
		keys = append(keys, key)
	}
	js, err := s.Journals(t.Context(), keys)
	must(t, err)
	if len(js) != 2 || js[0].OutsideWarnings.Error != "ledger_unavailable" || js[1].OutsideWarnings.Error != "" {
		t.Fatalf("queue contamination: %+v", js)
	}
	js, err = s.ReportingJournals(t.Context(), keys)
	must(t, err)
	if len(js) != 2 || js[0].OutsideWarnings.Error != "" {
		t.Fatal("worker reporting read ledger")
	}
}

func TestWarningCacheDelayedAuditRollbackAndAcknowledgement(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendWarningPin(t, s, q, authority.OpenMerge)
	seedOutsideLanding(t, s, q)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "delayed"})
	summary, err := s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if summary.Outstanding != 0 {
		t.Fatal(summary)
	}
	appendWarningPin(t, s, q, authority.GitHub)
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "abandoned"})
	auditPass(t, s, q)
	summary, err = s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if summary.Outstanding != 1 {
		t.Fatalf("late authority audit after rollback: %+v", summary)
	}
	_, err = s.AcknowledgeOutsidePush(t.Context(), q, summary.EventIDs[0], "delayed", "operator")
	must(t, err)
	summary, err = s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if summary.Outstanding != 0 {
		t.Fatal(summary)
	}
}

// Cancellation after a completely consumed page leaves a resumable index, but
// must not publish its partial counts. An empty successful suffix completes it.
type cancelWarningPageTrace struct {
	cancel context.CancelFunc
	pages  atomic.Int32
}

func (t *cancelWarningPageTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == warningPageSQL && t.pages.Add(1) == 2 {
		t.cancel()
	}
	return ctx
}
func (*cancelWarningPageTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestWarningCacheCompletesAfterCancelledPage(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	appendWarningPin(t, s, q, authority.OpenMerge)
	appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "outside"})
	auditPass(t, s, q)
	_, watermark, err := s.Load(t.Context(), q)
	must(t, err)
	events := make([]queue.Event, warningPageSize-int(watermark))
	for i := range events {
		events[i] = queue.RefreshRequested{}
	}
	appendEvents(t, s, q, events...)
	original := s.pool
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := original.Config().Copy()
	cfg.ConnConfig.Tracer = &cancelWarningPageTrace{cancel: cancel}
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	s.pool = traced
	t.Cleanup(original.Close)
	summary, err := s.ReadOutsidePushWarnings(ctx, q)
	if !errors.Is(err, context.Canceled) || summary.Error == "" || summary.Outstanding != 0 {
		t.Fatalf("partial snapshot published: %+v / %v", summary, err)
	}
	if s.warningCache(q).through != warningPageSize {
		t.Fatalf("cancellation did not exercise an already consumed full page: watermark=%d", s.warningCache(q).through)
	}
	key := QueueKey{}
	must(t, s.pool.QueryRow(t.Context(), `SELECT installation,repository,base,lane FROM queues WHERE id=$1`, q).Scan(&key.Installation, &key.Repository, &key.BaseBranch, &key.Lane))
	js, err := s.Journals(t.Context(), []QueueKey{key})
	must(t, err)
	if js[0].OutsideWarnings.Outstanding != 1 || js[0].OutsideWarnings.Error != "" {
		t.Fatalf("empty suffix failed to complete recovered index: %+v", js[0].OutsideWarnings)
	}
	summary, err = s.ReadOutsidePushWarnings(t.Context(), q)
	must(t, err)
	if summary.Outstanding != 1 || summary.Error != "" {
		t.Fatalf("background recovery failed: %+v", summary)
	}
}
