package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type journalReplayTrace struct {
	buffered, resume, writerWaiting chan struct{}
	writerOnce                      sync.Once
}

type journalReplayKey struct{}
type journalWriterKey struct{}

func (p *journalReplayTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "SELECT id, recorded_at, payload, dedupe_key FROM events") {
		return context.WithValue(ctx, journalReplayKey{}, true)
	}
	return ctx
}

func (p *journalReplayTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(journalReplayKey{}) == true {
		close(p.buffered)
		<-p.resume
	}
}

func (p *journalReplayTrace) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	if ctx.Value(journalWriterKey{}) == true {
		p.writerOnce.Do(func() { close(p.writerWaiting) })
	}
	return ctx
}

func (*journalReplayTrace) TraceAcquireEnd(context.Context, *pgxpool.Pool, pgxpool.TraceAcquireEndData) {
}

func TestJournalsCancellationReleasesWriterConnection(t *testing.T) {
	s := newStore(t)
	key := QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}
	id, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	other := key
	other.Repository = "other/repo"
	writerID, err := s.EnsureQueue(t.Context(), other)
	must(t, err)
	files := make([]string, 200)
	for i := range files {
		files[i] = fmt.Sprintf("src/representative/directory/component_%03d.go", i)
	}
	kind, raw, err := queue.EncodeEvent(queue.PRMetadataObserved{PR: 42, HeadSHA: "head", Title: "metadata", ApprovedReviewsBy: []string{}, Labels: []string{}, Files: files, FilesKnown: true})
	must(t, err)
	const count = 30000
	_, err = s.pool.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) SELECT $1,$2,$3::jsonb FROM generate_series(1,$4)`, id, kind, string(raw), count)
	must(t, err)

	trace := &journalReplayTrace{buffered: make(chan struct{}), resume: make(chan struct{}), writerWaiting: make(chan struct{})}
	cfg := s.pool.Config()
	cfg.MaxConns = 1
	cfg.ConnConfig.Tracer = trace
	s.pool.Close()
	s.pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var release sync.Once
	resume := func() { release.Do(func() { close(trace.resume) }) }
	t.Cleanup(resume)
	read := make(chan error, 1)
	go func() {
		_, err := s.Journals(ctx, []QueueKey{key})
		read <- err
	}()
	select {
	case <-trace.buffered:
	case err := <-read:
		t.Fatal("read ended before replay", err)
	case <-time.After(30 * time.Second):
		t.Fatal("journal transfer did not finish")
	}
	if s.pool.Stat().AcquiredConns() != 1 {
		t.Fatal("journal did not retain the only connection")
	}
	writer := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), journalWriterKey{}, true), 2*time.Second)
		defer cancel()
		_, err := s.Append(ctx, writerID, []queue.Event{queue.RefreshRequested{}}, "after-cancel")
		writer <- err
	}()
	<-trace.writerWaiting
	start := time.Now()
	cancel()
	resume()
	if err := <-writer; err != nil {
		t.Error("waiting writer did not get the connection after cancellation:", err)
	}
	if err := <-read; !errors.Is(err, context.Canceled) {
		t.Error("read did not return cancellation:", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Error("canceled replay retained the connection:", elapsed)
	} else {
		t.Logf("canceled buffered replay of %d events (%d bytes each); reader and writer finished in %s", count, len(raw), elapsed)
	}
	_, watermark, err := s.Load(t.Context(), writerID)
	must(t, err)
	if watermark == 0 {
		t.Fatal("writer did not commit")
	}
}
