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
	// Seed the historical admitted attempt before enabling authority; new
	// authority starts require the atomic admission gate.
	seedOutsideLanding(t, s, q)
	appendWarningPin(t, s, q, authority.OpenMerge)
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

// Cancellation after a consumed page discards the private refresh index.
// Foreground and the next refresh must still recover the complete snapshot.
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
	if s.warningCache(q).through != 0 {
		t.Fatal("cancelled refresh published a partial index")
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

// Pause real background SQL after the refresh has started, without delaying the
// journal query. A foreground multi-queue read must not inherit that pause.
type pausedWarningRefreshTrace struct {
	sql     string
	entered chan struct{}
	release chan struct{}
	paused  atomic.Bool
}

func (p *pausedWarningRefreshTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	target := p.sql
	if target == "" {
		target = warningPageSQL
	}
	if data.SQL == target && p.paused.CompareAndSwap(false, true) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*pausedWarningRefreshTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestWarningRefreshNeverBlocksForegroundOrSibling(t *testing.T) {
	s := newStore(t)
	keys := []QueueKey{
		{Installation: "7", Repository: "owner/repo", BaseBranch: "main"},
		{Installation: "7", Repository: "owner/repo", BaseBranch: "release"},
	}
	var ids []QueueID
	for _, key := range keys {
		id, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		appendWarningPin(t, s, id, authority.OpenMerge)
		ids = append(ids, id)
	}
	appendEvents(t, s, ids[0], queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "outside"})
	auditPass(t, s, ids[0])
	_, err := s.Journals(t.Context(), keys)
	must(t, err)
	original := s.pool
	cfg := original.Config().Copy()
	pause := &pausedWarningRefreshTrace{entered: make(chan struct{}), release: make(chan struct{})}
	cfg.ConnConfig.Tracer = pause
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	s.pool = traced
	t.Cleanup(original.Close)
	done := make(chan error, 1)
	go func() { _, err := s.ReadOutsidePushWarnings(t.Context(), ids[0]); done <- err }()
	defer func() { close(pause.release); must(t, <-done) }()
	select {
	case <-pause.entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach SQL")
	}
	// The background refresh stays paused until this test returns, so a foreground
	// read that waited on it could never finish before the deadline. A generous
	// bound keeps that guarantee without racing slow CI runners.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan []Journal, 1)
	failures := make(chan error, 1)
	go func() { js, err := s.Journals(ctx, keys); result <- js; failures <- err }()
	select {
	case js := <-result:
		must(t, <-failures)
		if ctx.Err() != nil {
			t.Fatal("foreground exceeded deadline")
		}
		if len(js) != 2 || js[0].OutsideWarnings.Outstanding != 1 || js[0].OutsideWarnings.Error != "" || js[1].OutsideWarnings.Outstanding != 0 || js[1].OutsideWarnings.Error != "" {
			t.Fatalf("refresh affected foreground/sibling: %+v", js)
		}
	case <-ctx.Done():
		t.Fatal("foreground blocked on background I/O")
	}
	// Even CPU fold contention falls back immediately to a complete snapshot.
	c := s.warningCache(ids[0])
	c.mu.Lock()
	snapshot := s.journalWarnings(ctx, ids[0], []JournalEvent{{ID: c.through}})
	older := s.journalWarnings(ctx, ids[0], nil)
	c.mu.Unlock()
	if older.Outstanding != 0 || older.Error != "ledger_unavailable" {
		t.Fatal("older snapshot saw future warnings", older)
	}
	if snapshot.Outstanding != 1 || snapshot.Error != "ledger_unavailable" {
		t.Fatal(snapshot)
	}
}

type cancelAfterJournalCommitTrace struct{ cancel context.CancelFunc }

func (p *cancelAfterJournalCommitTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, journalCommitTraceKey{}, data.SQL == "commit")
}

type journalCommitTraceKey struct{}

func (p *cancelAfterJournalCommitTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if committed, _ := ctx.Value(journalCommitTraceKey{}).(bool); committed && data.Err == nil {
		p.cancel()
	}
}

func TestWarningFoldCancellationDoesNotFailJournals(t *testing.T) {
	s := newStore(t)
	keys := []QueueKey{
		{Installation: "7", Repository: "owner/repo", BaseBranch: "main"},
		{Installation: "7", Repository: "owner/repo", BaseBranch: "release"},
	}
	for i, key := range keys {
		id, err := s.EnsureQueue(t.Context(), key)
		must(t, err)
		if i == 0 {
			appendWarningPin(t, s, id, authority.OpenMerge)
		}
		appendEvents(t, s, id, queue.RefreshRequested{})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	original := s.pool
	cfg := original.Config().Copy()
	cfg.ConnConfig.Tracer = &cancelAfterJournalCommitTrace{cancel: cancel}
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	s.pool = traced
	t.Cleanup(original.Close)
	js, err := s.Journals(ctx, keys)
	must(t, err)
	if ctx.Err() == nil {
		t.Fatal("test failed to cancel at committed snapshot")
	}
	if len(js) != 2 || js[0].OutsideWarnings.Error != "ledger_unavailable" || js[1].OutsideWarnings.Error != "" || len(js[1].Events) != 1 {
		t.Fatalf("warning cancellation contaminated whole response: %+v", js)
	}
}

func TestWarningRefreshDoesNotReplaceNewerForeground(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("incomplete-%t", incomplete), func(t *testing.T) {
			s := newStore(t)
			key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}
			id, err := s.EnsureQueue(t.Context(), key)
			must(t, err)
			appendWarningPin(t, s, id, authority.OpenMerge)
			appendEvents(t, s, id, queue.BaseMoved{SHA: "base"}, queue.BaseMoved{SHA: "outside-1"})
			auditPass(t, s, id)
			_, err = s.Journals(t.Context(), []QueueKey{key})
			must(t, err)
			original := s.pool
			cfg := original.Config().Copy()
			pause := &pausedWarningRefreshTrace{sql: "commit", entered: make(chan struct{}), release: make(chan struct{})}
			cfg.ConnConfig.Tracer = pause
			traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
			must(t, err)
			s.pool = traced
			t.Cleanup(original.Close)
			done := make(chan OutsideWarningSummary, 1)
			failures := make(chan error, 1)
			go func() { summary, err := s.ReadOutsidePushWarnings(t.Context(), id); done <- summary; failures <- err }()
			released := false
			defer func() {
				if !released {
					close(pause.release)
					<-done
					<-failures
				}
			}()
			select {
			case <-pause.entered:
			case <-time.After(time.Second):
				t.Fatal("refresh did not reach commit")
			}
			appendEvents(t, s, id, queue.BaseMoved{SHA: "outside-2"})
			auditPass(t, s, id)
			if incomplete {
				// Interrupt a real foreground fold after its new audit but before completion.
				appendEvents(t, s, id, queue.RefreshRequested{})
				js, err := s.ReportingJournals(t.Context(), []QueueKey{key})
				must(t, err)
				c := s.warningCache(id)
				remaining := 0
				for _, e := range js[0].Events {
					if e.ID > c.through {
						remaining++
					}
				}
				ctx := &cancelWarningFoldContext{Context: t.Context(), remaining: remaining - 1}
				summary := s.journalWarnings(ctx, id, js[0].Events)
				if summary.Error != "ledger_unavailable" || c.through <= c.published.Load().through {
					t.Fatal("test failed to interrupt an advanced fold", summary)
				}
			} else {
				js, err := s.Journals(t.Context(), []QueueKey{key})
				must(t, err)
				if js[0].OutsideWarnings.Outstanding != 2 {
					t.Fatal(js[0].OutsideWarnings)
				}
			}
			close(pause.release)
			released = true
			summary := <-done
			must(t, <-failures)
			if incomplete {
				if summary.Outstanding != 1 || summary.Error != "ledger_refreshing" {
					t.Fatal("older snapshot lacks stale marker", summary)
				}
			} else if summary.Outstanding != 2 || summary.Error != "" {
				t.Fatal("refresh republished older counts", summary)
			}
			js, err := s.Journals(t.Context(), []QueueKey{key})
			must(t, err)
			if js[0].OutsideWarnings.Outstanding != 2 {
				t.Fatal("refresh replaced newer index", js[0].OutsideWarnings)
			}

		})
	}
}

// Cancel only between fold units, independent of SQL scheduling.
type cancelWarningFoldContext struct {
	context.Context
	remaining int
}

func (c *cancelWarningFoldContext) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return nil
}
