package serve

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := fixture(t, repositoryConfig)
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
		testSynctest(t, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
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
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig)
				s.options.Health.MaxSuccessAge = maxAge
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				for n := 2; n <= 71; n++ {
					_, err = f.OpenPR(repo, n, base, true)
					must(t, err)
				}
				started := time.Now()
				s.publishHealth(w, w.logState, true, nil, started)
				s.options.PollInterval = time.Hour
				d := &reconciliationHealthDriver{Driver: w.Driver, entered: make(chan int, 71), hang: 71}
				w.Driver = d
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); s.loop(ctx, w) }()
				defer func() { cancel(); <-done }()
				for n := 1; n <= 70; n++ {
					if got := <-d.entered; got != n {
						t.Fatalf("read %d, want %d", got, n)
					}
					time.Sleep(time.Minute)
					assertHealthCode(t, s, 200)
				}
				if got := <-d.entered; got != 71 {
					t.Fatal(got)
				}
				if w.health.Load().LastSuccess.After(started) {
					t.Fatal("progress fabricated reconciliation success")
				}
				// Recent completed work cannot bypass the absolute cycle backstop.
				current := *w.health.Load()
				overdue := current
				longAgo := time.Now().Add(-s.options.Health.MaxCycleDuration - time.Second)
				overdue.CycleStartedAt = &longAgo
				w.health.Store(&overdue)
				assertHealthCode(t, s, 503)
				w.health.Store(&current)
				assertHealthCode(t, s, 200)
				time.Sleep(s.options.Health.ProgressStallLimit + time.Second)
				assertHealthCode(t, s, 503)
				q := status.EvaluateHealth(*w.health.Load(), s.options.Health, time.Now())
				if !q.Overdue || q.Degraded {
					t.Fatalf("stall: %+v", q)
				}
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

// Each delay is one completed forge operation, not an artificial progress tick.
type reconciliationHealthDriver struct {
	forge.Driver
	entered   chan int
	completed atomic.Int32
	hang      int
}

func (d *reconciliationHealthDriver) PullRequest(ctx context.Context, r forge.RepoRef, n int) (forge.PullRequest, error) {
	d.entered <- n
	if n == d.hang {
		<-ctx.Done()
		return forge.PullRequest{}, ctx.Err()
	}
	select {
	case <-time.After(time.Minute):
	case <-ctx.Done():
		return forge.PullRequest{}, ctx.Err()
	}
	pr, err := d.Driver.PullRequest(ctx, r, n)
	if err == nil {
		d.completed.Add(1)
	}
	return pr, err
}

func TestHealthRealReconciliationProgressAndHungRead(t *testing.T) {
	for _, hung := range []bool{false, true} {
		t.Run(fmt.Sprintf("hung-%t", hung), func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig)
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				for n := 2; n <= 4; n++ {
					_, err := f.OpenPR(repo, n, base, true)
					must(t, err)
				}
				// Reproduce the review's 151s threshold with individually bounded 60s reads.
				s.options.ActionTimeout = time.Minute
				s.options.Health.ProgressStallLimit = 150 * time.Second
				must(t, ValidateHealthSettings(s.options.Health, s.options.ActionTimeout))
				s.options.PollInterval = time.Hour
				s.publishHealth(w, w.logState, true, nil, time.Now())
				d := &reconciliationHealthDriver{Driver: w.Driver, entered: make(chan int, 4)}
				if hung {
					d.hang = 3
				}
				w.Driver = d
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); s.loop(ctx, w) }()
				defer func() { cancel(); <-done }()
				for n := 1; n <= 4; n++ {
					if got := <-d.entered; got != n {
						t.Fatalf("read %d, want %d", got, n)
					}
					synctest.Wait()
					before := *w.health.Load().LastProgressAt
					if hung && n == 3 {
						time.Sleep(s.options.Health.ProgressStallLimit - time.Second)
						assertHealthCode(t, s, 200)
						time.Sleep(2 * time.Second)
						assertHealthCode(t, s, 503)
						if *w.health.Load().LastProgressAt != before || d.completed.Load() != 2 {
							t.Fatal("hung read refreshed progress")
						}
						return
					}
					// Check health throughout the actual reconciliation, including t=151s.
					for _, seconds := range []time.Duration{15, 16, 29} {
						time.Sleep(seconds * time.Second)
						assertHealthCode(t, s, 200)
					}
				}
				synctest.Wait()
				if d.completed.Load() != 4 {
					t.Fatalf("completed reads: %d", d.completed.Load())
				}
				assertHealthCode(t, s, 200)
			})
		})
	}
}

func TestHealthActionBudgetValidation(t *testing.T) {
	for _, action := range []time.Duration{0, time.Minute, DefaultActionTimeout} {
		s, err := newServer(nil, Options{ActionTimeout: action})
		must(t, err)
		if s.options.Health.ProgressStallLimit != 4*time.Minute {
			t.Fatal(s.options.Health)
		}
	}
	for _, explicit := range []bool{false, true} {
		opts := Options{ActionTimeout: time.Hour}
		if explicit {
			opts.Health = status.DefaultHealthSettings()
		}
		if _, err := newServer(nil, opts); err == nil || !strings.Contains(err.Error(), "git maintenance 30s + completion write 10s + grace 30s") {
			t.Fatalf("accepted incompatible timeout: %v", err)
		}
	}
	settings := status.DefaultHealthSettings()
	settings.ProgressStallLimit = DefaultActionTimeout + 30*time.Second + completionWriteTimeout + settings.OverdueGrace
	_, err := newServer(nil, Options{Health: settings})
	must(t, err)
	settings.ProgressStallLimit -= time.Nanosecond
	if _, err := newServer(nil, Options{Health: settings}); err == nil {
		t.Fatal("accepted insufficient budget")
	}
	settings.ProgressStallLimit = 30 * time.Minute
	_, err = newServer(nil, Options{ActionTimeout: 28 * time.Minute, Health: settings})
	must(t, err)
}

// Delay each source independently; an aggregate PR/rules/report loop is not a
// bounded progress unit. The real cycle must publish between these operations.
type unitHealthDriver struct {
	forge.Driver
	start chan string
}

func (d *unitHealthDriver) delay(unit string) {
	d.start <- unit
	time.Sleep(time.Minute)
}
func (d *unitHealthDriver) DefaultBranch(ctx context.Context, r forge.RepoRef) (string, error) {
	d.delay("default branch")
	return d.Driver.DefaultBranch(ctx, r)
}
func (d *unitHealthDriver) ReadFile(ctx context.Context, r forge.RepoRef, ref, path string) ([]byte, error) {
	data, err := d.Driver.ReadFile(ctx, r, ref, path)
	unit := "configuration file"
	if err != nil {
		unit = "configuration file missing"
	}
	d.delay(unit)
	return data, err
}
func (d *unitHealthDriver) PullRequest(ctx context.Context, r forge.RepoRef, n int) (forge.PullRequest, error) {
	d.delay("PR")
	return d.Driver.PullRequest(ctx, r, n)
}
func (d *unitHealthDriver) ListComments(ctx context.Context, r forge.RepoRef, n int, since time.Time) ([]forge.Comment, error) {
	d.delay("comments")
	return d.Driver.ListComments(ctx, r, n, since)
}
func (d *unitHealthDriver) CheckRuns(ctx context.Context, r forge.RepoRef, sha string) ([]forge.CheckRun, error) {
	d.delay("check source")
	return d.Driver.CheckRuns(ctx, r, sha)
}
func (d *unitHealthDriver) ReportStatus(ctx context.Context, r forge.RepoRef, n int, sha string, value forge.Status) error {
	d.delay("check report")
	return d.Driver.ReportStatus(ctx, r, n, sha, value)
}
func TestHealthRealCyclePublishesEachReadAndReport(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig+"    enter_when:\n      - check-success = ci\n")
		base, err := f.ReadRef(t.Context(), repo, "main")
		must(t, err)
		for n := 2; n <= 4; n++ {
			_, err = f.OpenPR(repo, n, base, true)
			must(t, err)
		}
		_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.PauseRequested{Reason: "operator", Detail: "test reporting"}}, "")
		must(t, err)
		s.publishHealth(w, w.logState, true, nil, time.Now())
		w.publishCycleStarted(time.Now())
		d := &unitHealthDriver{Driver: w.Driver, start: make(chan string, 64)}
		w.Driver = d
		check := func(name string) func(context.Context) ([]string, []string, error) {
			return func(context.Context) ([]string, []string, error) { d.delay(name); return nil, nil, nil }
		}
		w.CheckRules = check("rules")
		w.CheckPermissions = check("permissions")
		w.CheckProtection = func(ctx context.Context, _ forge.MergeMethod) ([]string, []string, error) {
			return check("protection")(ctx)
		}
		w.rulesNextCheck = time.Time{}
		s.options.RulesInterval = time.Hour
		done := make(chan error, 1)
		go func() { done <- s.cycle(t.Context(), w) }()
		finished := false
		defer func() {
			if !finished {
				<-done
			}
		}()
		seen := map[string]int{}
		units := 0
		previous := ""
		for {
			select {
			case unit := <-d.start:
				seen[unit]++
				if units > 0 && previous != "configuration file missing" && time.Since(*w.health.Load().LastProgressAt) > 0 {
					t.Errorf("no progress before next %s read: %s", unit, time.Since(*w.health.Load().LastProgressAt))
				}
				if previous == "configuration file missing" && strings.HasPrefix(unit, "configuration file") && w.health.Load().LastProgressAt.Equal(time.Now()) {
					t.Error("failed configuration file read published progress")
				}
				previous = unit
				units++
				for range 3 {
					time.Sleep(20 * time.Second)
					assertHealthCode(t, s, 200)
				}
			case err := <-done:
				finished = true
				must(t, err)
				for _, unit := range []string{"rules", "permissions", "protection", "default branch", "configuration file", "PR", "comments", "check source", "check report"} {
					if seen[unit] == 0 {
						t.Fatalf("cycle did not exercise %s: %v", unit, seen)
					}
				}
				if seen["PR"] != 4 || seen["check report"] != 4 {
					t.Fatalf("incomplete PR/report loops: %v", seen)
				}
				t.Logf("completed units: %v", seen)
				return
			}
		}
	})
}
