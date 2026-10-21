package serve

import (
	"context"
	"math"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

type stalledHealthDriver struct{ forge.Driver }

func (d stalledHealthDriver) ListOpenPullRequests(ctx context.Context, _ forge.RepoRef, _ string) ([]forge.PullRequestSummary, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func assertHealthCode(t *testing.T, s *Server, code int) {
	t.Helper()
	rr := httptest.NewRecorder()
	status.DynamicHandler(s, s.Keys, "", nil).ServeHTTP(rr, httptest.NewRequest("GET", "/v1/health", nil))
	if rr.Code != code {
		t.Fatalf("health=%d want %d: %s", rr.Code, code, rr.Body)
	}
}

func TestHealthCycleRuntimeStartsAfterSlotAcquisition(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	synctest.Test(t, func(t *testing.T) {
		s.options.Health.ProgressStallLimit = 2 * time.Minute
		s.options.Health.MaxSuccessAge = time.Hour
		w.Driver = stalledHealthDriver{w.Driver}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); s.loop(ctx, w) }()
		defer func() { cancel(); <-done }()
		synctest.Wait()
		if q := w.health.Load(); !q.InCycle || q.CycleStartedAt == nil || q.NextDeadline != nil {
			t.Fatalf("first delay=0 cycle: %+v", q)
		}
		assertHealthCode(t, s, 200)
		time.Sleep(s.options.Health.OverdueGrace + time.Second)
		assertHealthCode(t, s, 200)
		time.Sleep(2 * time.Minute)
		assertHealthCode(t, s, 503)
		q := status.EvaluateHealth(*w.health.Load(), s.options.Health, time.Now())
		if !q.Overdue || q.Degraded {
			t.Fatalf("progress stall limit: %+v", q)
		}
	})
}

func TestHealthSlotStarvationAndImmediateJournalWake(t *testing.T) {
	for _, journalWake := range []bool{false, true} {
		s, w, _, _ := fixture(t, repositoryConfig)
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			if journalWake {
				// Enter the actual long backstop, then consume every slot before appending.
				startIdleBackstop(t, s, w)
				for range cap(s.slots) {
					s.slots <- struct{}{}
				}
				_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.RefreshRequested{}}, "")
				must(t, err)
				time.Sleep(s.options.PollInterval)
				synctest.Wait()
				// startIdleBackstop owns cancellation and draining of its loop.
				close(done)
			} else {
				s.slots = make(chan struct{}, 1)
				s.slots <- struct{}{}
				go func() { defer close(done); s.loop(ctx, w) }()
				synctest.Wait()
			}
			defer func() {
				cancel()
				if journalWake {
					for range cap(s.slots) {
						<-s.slots
					}
				}
				<-done
			}()
			before := time.Now()
			q := w.health.Load()
			if q.InCycle || q.NextDeadline == nil || q.NextDeadline.After(before) {
				t.Fatalf("missing immediate start deadline: %+v", q)
			}
			time.Sleep(s.options.Health.OverdueGrace + time.Second)
			assertHealthCode(t, s, 503)
			q = w.health.Load()
			if status.EvaluateHealth(*q, s.options.Health, time.Now()).Overdue != true {
				t.Fatal("slot starvation not overdue")
			}
		})
	}
}

func TestHealthRecoveryLoopExplicitlyExcludesSettledWorker(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	w.recovery = true
	w.managed = true
	// Simulate stale best-effort logging: durable state is settled, but logging
	// retained the old landing and cannot advance its cursor on this turn.
	w.logState.Active = &queue.Batch{Phase: queue.Landing, LandingAttempt: queue.LandingAttempt{ID: "stale"}}
	w.logWatermark = math.MaxInt64
	s.publishHealth(w, w.logState, false, nil, time.Now().Add(-time.Hour))
	if !w.health.Load().Watched {
		t.Fatal("pending recovery excluded early")
	}
	done := make(chan struct{})
	go func() { defer close(done); s.loop(t.Context(), w) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("settled recovery did not exit")
	}
	if w.health.Load().Watched {
		t.Fatal("exited recovery remains watched")
	}
	assertHealthCode(t, s, 200)
}

func TestHealthLongProgressingCycle(t *testing.T) {
	for _, maxAge := range []time.Duration{15 * time.Minute, time.Hour} {
		t.Run(maxAge.String(), func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			s.options.Health.MaxSuccessAge = maxAge
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				s.publishHealth(w, w.logState, true, nil, started)
				w.publishCycleStarted(started)
				// Both the default 15m and maximum configurable 60m success ages elapse.
				for minute := 1; minute <= 70; minute++ {
					time.Sleep(time.Minute)
					w.publishProgress(time.Now())
					assertHealthCode(t, s, 200)
				}
				if w.health.Load().LastSuccess.After(started) {
					t.Fatal("progress fabricated reconciliation success")
				}
				time.Sleep(s.options.Health.ProgressStallLimit + time.Second)
				assertHealthCode(t, s, 503)
				q := status.EvaluateHealth(*w.health.Load(), s.options.Health, time.Now())
				if !q.Overdue || q.Degraded {
					t.Fatalf("stall: %+v", q)
				}
				w.publishProgress(time.Now())
				assertHealthCode(t, s, 200)
				// Progress cannot bypass the absolute cycle backstop.
				old := *w.health.Load()
				longAgo := time.Now().Add(-s.options.Health.MaxCycleDuration - time.Second)
				old.CycleStartedAt = &longAgo
				w.health.Store(&old)
				assertHealthCode(t, s, 503)
			})
		})
	}
}

func TestHealthCompletedActionAndTurnPublishProgress(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	// Seed a real planned action without running a whole cycle first.
	must(t, s.reconcile(t.Context(), w, false))
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.reconcile(t.Context(), w, true))
	cfg := w.plannerConfig(s.options.PollInterval)
	_, err := s.store.Plan(t.Context(), w.id, cfg, time.Now())
	must(t, err)
	claims, err := s.store.ClaimQueue(t.Context(), w.id, 1, time.Minute, time.Now())
	must(t, err)
	if len(claims) != 1 {
		t.Fatalf("missing planned action: %+v", claims)
	}
	old := time.Now().Add(-time.Hour)
	w.publishCycleStarted(old)
	must(t, s.execute(t.Context(), w, claims[0]))
	afterAction := w.health.Load().LastProgressAt
	if afterAction == nil || !afterAction.After(old) {
		t.Fatal("completed action did not publish progress")
	}
	q := *w.health.Load()
	q.LastProgressAt = &old
	w.health.Store(&q)
	must(t, s.cycle(t.Context(), w))
	if progress := w.health.Load().LastProgressAt; progress == nil || !progress.After(old) {
		t.Fatal("completed turn did not publish progress")
	}
}
