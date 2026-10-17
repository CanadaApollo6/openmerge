package store

import (
	"testing"
	"time"
)

func TestReviewJournalReadHasServerTimeouts(t *testing.T) {
	url := databaseURL(t)
	s := openTestStore(t, url)
	key := QueueKey{Installation: "1", Repository: "review/test", BaseBranch: "main"}
	id, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	appendEvents(t, s, id, seedEvents()...)
	r, err := OpenJournalReader(t.Context(), url, DefaultJournalReadTimeout)
	must(t, err)
	defer r.Close()
	_, err = r.Journals(t.Context(), []QueueKey{key})
	must(t, err)
	if n := r.store.pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("read retained %d connections", n)
	}
	for _, setting := range []string{"statement_timeout", "lock_timeout", "idle_in_transaction_session_timeout"} {
		var value string
		must(t, r.store.pool.QueryRow(t.Context(), "SHOW "+setting).Scan(&value))
		if value == "0" {
			t.Errorf("%s is unbounded", setting)
		}
	}
}

// Reproduces the review's pinned snapshot behind an exclusive event-table lock,
// but requires the reader to return without external cancellation.
func TestReviewJournalBlockedReadExpires(t *testing.T) {
	url := databaseURL(t)
	s := openTestStore(t, url)
	key := QueueKey{Installation: "1", Repository: "review/blocked", BaseBranch: "main"}
	id, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	appendEvents(t, s, id, seedEvents()...)
	r, err := OpenJournalReader(t.Context(), url, 100*time.Millisecond)
	must(t, err)
	defer r.Close()
	blocker, err := s.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = blocker.Rollback(t.Context()) }()
	_, err = blocker.Exec(t.Context(), "LOCK TABLE events IN ACCESS EXCLUSIVE MODE")
	must(t, err)
	done := make(chan error, 1)
	go func() { _, readErr := r.Journals(t.Context(), []QueueKey{key}); done <- readErr }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked journal read succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("journal snapshot outlived its bound")
	}
	// pgx asynchronously destroys a connection closed by context cancellation.
	deadline := time.Now().Add(time.Second)
	for r.store.pool.Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := r.store.pool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("timed out read retained %d connections", n)
	}
}
func TestJournalReaderRejectsUnboundedTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, time.Nanosecond} {
		if _, err := OpenJournalReader(t.Context(), "postgres://unused", timeout); err == nil {
			t.Fatal("unbounded timeout accepted", timeout)
		}
	}
}
