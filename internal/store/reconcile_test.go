package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestPausedConsequentialClaimDefersUntilResume(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		t.Run(fmt.Sprint(superseded), func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch})
			_, err := s.Plan(t.Context(), q, config, epoch)
			must(t, err)
			claims, err := s.ClaimQueue(t.Context(), q, 1, time.Minute, epoch)
			must(t, err)
			claim := claims[0]
			appendEvents(t, s, q, queue.PauseRequested{Reason: "operator", Detail: "repairing base"})
			if superseded {
				appendEvents(t, s, q, queue.PRObserved{PR: 1, HeadSHA: "new-head", Eligible: true, Trusted: true})
			}
			proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
			if superseded {
				must(t, err)
				if proceed || count(t, s, `SELECT count(*) FROM actions WHERE key = $1 AND status = 'cancelled'`, claim.Key) != 1 {
					t.Fatal("superseded paused work was not cancelled")
				}
				return
			}
			if proceed || !errors.Is(err, ErrActionDeferred) {
				t.Fatalf("paused claim was consumed: proceed=%v error=%v", proceed, err)
			}
			must(t, s.Complete(t.Context(), claim, Failed))
			if count(t, s, `SELECT count(*) FROM actions WHERE key = $1 AND status = 'planned' AND poison_count = 0`, claim.Key) != 1 {
				t.Fatal("deferral consumed retry budget or retained claim")
			}
			claims, err = s.ClaimQueue(t.Context(), q, 1, time.Minute, epoch, claim.Key)
			must(t, err)
			if len(claims) != 0 {
				t.Fatal("same-cycle deferral exclusion ignored")
			}
			appendEvents(t, s, q, queue.ResumeRequested{})
			claims, err = s.ClaimQueue(t.Context(), q, 1, time.Minute, epoch)
			must(t, err)
			if len(claims) != 1 {
				t.Fatal("resumed action was lost")
			}
			proceed, err = s.BeginConsequential(t.Context(), claims[0], config, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("resumed action did not run")
			}
		})
	}
}

func TestHeadCommandObservationTiming(t *testing.T) {
	for _, tt := range []struct {
		name    string
		heads   []string
		times   []time.Duration
		changed bool
	}{
		{"first discovery", nil, nil, false},
		{"old head before command", []string{"a", "b"}, []time.Duration{-10 * time.Second, 2 * time.Second}, true},
		{"reissued after stable observation", []string{"a", "b"}, []time.Duration{-10 * time.Second, -2 * time.Second}, false},
		{"change near command", []string{"a", "b"}, []time.Duration{-10 * time.Second, -500 * time.Millisecond}, true},
		{"old head near command", []string{"a"}, []time.Duration{500 * time.Millisecond}, true},
		{"old head at margin", []string{"a"}, []time.Duration{time.Second}, true},
		{"only evidence after margin", []string{"a", "b"}, []time.Duration{2 * time.Second, 3 * time.Second}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			for i, head := range tt.heads {
				id := appendEvents(t, s, q, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
				_, err := s.pool.Exec(t.Context(), `UPDATE events SET recorded_at = $2 WHERE id = $1`, id, epoch.Add(tt.times[i]))
				must(t, err)
			}
			// Other PRs must not supply evidence about this command.
			id := appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "other", Eligible: true, Trusted: true})
			_, err := s.pool.Exec(t.Context(), `UPDATE events SET recorded_at = $2 WHERE id = $1`, id, epoch)
			must(t, err)
			changed, err := s.HeadChangedSince(t.Context(), q, 1, "b", epoch)
			must(t, err)
			if changed != tt.changed {
				t.Fatalf("changed=%v, want %v", changed, tt.changed)
			}
		})
	}
}

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
