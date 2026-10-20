package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAuthorityStatusAndHealthDoNotWaitForWorker(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	read := func(name string) queue.Event {
		raw, err := os.ReadFile("../queue/testdata/wire/v1/" + name + ".json")
		must(t, err)
		e, err := queue.DecodeEvent(raw)
		must(t, err)
		return e
	}
	pin := read("event_policy_authority_applied").(queue.PolicyAuthorityApplied)
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin}, "")
	must(t, err)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	s.publishHealth(w, state, false, nil, time.Now())
	handler := status.DynamicHandler(s, func() []store.QueueKey { return s.Keys() }, "secret", nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, path := range []string{"/v1/live", "/v1/health", "/v1/queues/owner/repo/main", "/v1/queues"} {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer secret")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, r)
			done <- rr
		}()
		select {
		case rr := <-done:
			if rr.Code != 200 {
				t.Fatalf("%s: %d %s", path, rr.Code, rr.Body)
			}
			if path != "/v1/live" && !json.Valid(rr.Body.Bytes()) {
				t.Fatal(rr.Body)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("%s blocked on worker lock", path)
		}
	}
}

func TestHealthDatabaseFailureAndLive(t *testing.T) {
	s, _, _, _ := fixture(t, repositoryConfig)
	s.store.Close()
	h := status.DynamicHandler(s, func() []store.QueueKey { panic("probe discovers keys") }, "secret", nil)
	for _, path := range []string{"/v1/live", "/v1/health"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		want := 200
		if path == "/v1/health" {
			want = 503
		}
		if rr.Code != want {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body)
		}
	}
}

func TestHealthPublicationLifecycleAndFailures(t *testing.T) {
	s, err := newServer(nil, Options{})
	must(t, err)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	w := &worker{}
	state := queue.State{}
	s.publishHealth(w, state, false, nil, now)
	if w.health.Load().LastSuccess != nil {
		t.Fatal("report-only turn claimed success")
	}
	s.publishHealth(w, state, true, nil, now)
	for i := 0; i < 3; i++ {
		s.publishHealth(w, state, false, context.DeadlineExceeded, now.Add(time.Duration(i)*time.Minute))
	}
	q := status.EvaluateHealth(*w.health.Load(), s.options.Health, now.Add(3*time.Minute))
	if !q.Degraded || q.LastSuccess == nil || !q.LastSuccess.Equal(now) {
		t.Fatalf("%+v", q)
	}
	s.publishHealth(w, state, true, nil, now.Add(4*time.Minute))
	if w.health.Load().Failures != 0 {
		t.Fatal("success did not reset failures")
	}
	for _, lifecycle := range []string{"dormant", "retired"} {
		retired := queue.Apply(state, queue.LifecycleChanged{State: lifecycle, Detail: "fixture"})
		s.publishHealth(w, retired, false, nil, now)
		if w.health.Load().Watched {
			t.Fatal("watched queue without obligations")
		}
		retired.Active = &queue.Batch{Phase: queue.Landing, LandingAttempt: queue.LandingAttempt{ID: "pending"}}
		s.publishHealth(w, retired, true, nil, now)
		if !w.health.Load().Watched {
			t.Fatal("ignored unsettled retired queue")
		}
	}
	w.settlementOnly = true
	s.publishHealth(w, state, false, nil, now)
	if w.health.Load().Watched {
		t.Fatal("watched settled recovery queue")
	}
}

func TestHealthActualBackstopDeadlineAndWebhookSilence(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	synctest.Test(t, func(t *testing.T) {
		polls := startIdleBackstop(t, s, w)
		first := *w.health.Load()
		if first.LastSuccess == nil || first.NextDeadline == nil {
			t.Fatalf("missing actual scheduler values: %+v", first)
		}
		expected := first.LastSuccess.Add(s.options.BackstopInterval)
		if !first.NextDeadline.Equal(expected) {
			t.Fatalf("deadline %v, want %v", first.NextDeadline, expected)
		}
		time.Sleep(s.options.BackstopInterval - 3*s.options.PollInterval)
		synctest.Wait()
		requireBackstopPoll(t, polls)
		second := w.health.Load()
		if !second.LastSuccess.After(*first.LastSuccess) {
			t.Fatal("backstop silence did not reconcile")
		}
		if first.NextDeadline.Equal(*second.NextDeadline) {
			t.Fatal("deadline was not renewed")
		}
		h, err := s.Health(t.Context())
		must(t, err)
		if !h.Healthy || len(h.Queues) != 1 || h.Queues[0].Overdue {
			t.Fatalf("%+v", h)
		}
	})
}

func TestHealthActualErrorAndQuotaDeadline(t *testing.T) {
	for _, quota := range []bool{false, true} {
		s, w, _, _ := fixture(t, repositoryConfig)
		synctest.Test(t, func(t *testing.T) {
			now := time.Now()
			failure := context.DeadlineExceeded
			delay := 5 * time.Second
			if quota {
				delay = time.Hour
				failure = &quotaBackoff{until: now.Add(delay)}
			}
			polls := make(chan struct{}, 16)
			w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls, err: failure}
			w.wake = make(chan struct{}, 1)
			s.slots = make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); s.loop(ctx, w) }()
			defer func() { cancel(); <-done }()
			synctest.Wait()
			requireBackstopPoll(t, polls)
			q := w.health.Load()
			if q.LastSuccess != nil || q.Failures != 1 || q.NextDeadline == nil || !q.NextDeadline.Equal(now.Add(delay)) {
				t.Fatalf("wrong failed scheduler snapshot: %+v", q)
			}
			w.wake <- struct{}{}
			synctest.Wait()
			if !w.health.Load().NextDeadline.Equal(now.Add(delay)) {
				t.Fatal("wake shortened quota/error delay")
			}
			if !quota {
				time.Sleep(5 * time.Second)
				synctest.Wait()
				requireBackstopPoll(t, polls)
				time.Sleep(10 * time.Second)
				synctest.Wait()
				requireBackstopPoll(t, polls)
				q := status.EvaluateHealth(*w.health.Load(), s.options.Health, time.Now())
				if !q.Degraded || q.Overdue {
					t.Fatalf("on-time failures not degraded: %+v", q)
				}
			}
		})
	}
}

func TestHealthHTTPWatchedConditionsAndIsolation(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	now := time.Now()
	overdue := now.Add(-time.Minute)
	old := now.Add(-time.Hour)
	healthy := status.HealthQueue{Installation: w.Key.Installation, Repository: w.Key.Repository, Base: w.Key.BaseBranch, Watched: true, StartedAt: now, LastSuccess: &now, Readiness: status.Readiness{Ready: true}}
	other := &worker{Target: Target{Key: store.QueueKey{Installation: "8", Repository: "other/repo", BaseBranch: "release"}}}
	other.health.Store(&healthy)
	s.queues = append(s.queues, other)
	handler := status.DynamicHandler(s, s.Keys, "secret", nil)
	for _, tc := range []struct {
		name   string
		change func(*status.HealthQueue)
		want   int
	}{
		{"healthy", func(*status.HealthQueue) {}, 200},
		{"unverified", func(q *status.HealthQueue) { q.Authority = &status.Authority{State: "unverified"} }, 200},
		{"warnings", func(q *status.HealthQueue) {
			q.OutsideWarnings = &status.OutsideWarnings{Outstanding: 90, EventIDs: []int64{1}, Truncated: true}
		}, 200},
		{"violated", func(q *status.HealthQueue) { q.Authority = &status.Authority{State: "violated"} }, 503},
		{"overdue", func(q *status.HealthQueue) { q.NextDeadline = &overdue }, 503},
		{"degraded age", func(q *status.HealthQueue) { q.LastSuccess = &old }, 503},
		{"degraded failures", func(q *status.HealthQueue) { q.Failures = 3 }, 503},
		{"retired without obligations", func(q *status.HealthQueue) {
			q.Watched = false
			q.Lifecycle = "retired"
			q.Authority = &status.Authority{State: "violated"}
			q.NextDeadline = &overdue
		}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := healthy
			tc.change(&q)
			w.health.Store(&q)
			r := httptest.NewRequest("GET", "/v1/health", nil)
			r.Header.Set("Authorization", "Bearer secret")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, r)
			if rr.Code != tc.want {
				t.Fatalf("%d %s", rr.Code, rr.Body)
			}
			var h status.Health
			must(t, json.Unmarshal(rr.Body.Bytes(), &h))
			if len(h.Queues) != 2 || h.Queues[1].Failing() {
				t.Fatal("one queue's health contaminated its sibling")
			}
			if tc.name == "warnings" && (h.Queues[0].OutsideWarnings == nil || h.Queues[0].OutsideWarnings.Outstanding != 90) {
				t.Fatal("lost outside warnings")
			}
		})
	}
	s.queues = nil
	h, err := s.Health(t.Context())
	must(t, err)
	if !h.Healthy || len(h.Queues) != 0 {
		t.Fatal("zero queues are not healthy")
	}
}

func TestHealthActualValidationAndRulesRetryDeadlines(t *testing.T) {
	for _, validation := range []bool{false, true} {
		s, w, _, _ := fixture(t, repositoryConfig)
		synctest.Test(t, func(t *testing.T) {
			now := time.Now()
			s.options.PollInterval = time.Hour
			s.options.RulesInterval = 2 * time.Second
			if validation {
				w.managed = true
				w.ready = false
				w.waiting = "validation pending"
				w.validationNext = now.Add(2 * time.Second)
			} else {
				w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, context.DeadlineExceeded }
				w.rulesNextCheck = time.Time{}
			}
			w.wake = make(chan struct{}, 1)
			s.slots = make(chan struct{}, 1)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); s.loop(ctx, w) }()
			defer func() { cancel(); <-done }()
			synctest.Wait()
			q := w.health.Load()
			if q.NextDeadline == nil || !q.NextDeadline.Equal(now.Add(2*time.Second)) || q.LastSuccess != nil {
				t.Fatalf("invalid validation snapshot: %+v", q)
			}
			if !validation {
				for range 2 {
					time.Sleep(2 * time.Second)
					synctest.Wait()
				}
				q := status.EvaluateHealth(*w.health.Load(), s.options.Health, time.Now())
				if !q.Degraded || q.LastSuccess != nil {
					t.Fatalf("failed rules reads claimed success: %+v", q)
				}
			}
		})
	}
}
