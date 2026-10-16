package store

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRecordTransitionsAndCommandReceipts(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	for _, value := range []string{"a", "a", "b", "a", "a"} {
		must(t, s.Record(t.Context(), q, "base", []byte(value), queue.BaseMoved{SHA: value}))
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q); n != 3 {
		t.Fatalf("changed observations=%d", n)
	}
	for range 2 {
		must(t, s.Record(t.Context(), q, "comment/1", []byte("true"), queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch}))
	}
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if len(state.Entries) != 1 {
		t.Fatalf("command not applied: %+v", state)
	}
	if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q); n != 5 {
		t.Fatalf("duplicate receipt appended events: %d", n)
	}
	value, err := s.Observation(t.Context(), q, "base")
	must(t, err)
	if string(value) != "a" {
		t.Fatalf("stored observation=%q", value)
	}
}

func TestNotificationsPersistAndDeduplicate(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	must(t, s.Notify(t.Context(), q, "delivery"))
	requested, err := s.TakeNotification(t.Context(), q)
	must(t, err)
	if !requested {
		t.Fatal("notification lost")
	}
	must(t, s.Notify(t.Context(), q, "delivery"))
	requested, err = s.TakeNotification(t.Context(), q)
	must(t, err)
	if requested {
		t.Fatal("duplicate webhook caused another reconcile")
	}
	must(t, s.Notify(t.Context(), q, "second"))
	reopened := openTestStore(t, s.pool.Config().ConnString())
	requested, err = reopened.TakeNotification(t.Context(), q)
	must(t, err)
	if !requested {
		t.Fatal("notification did not survive reconnect")
	}
}
