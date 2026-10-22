package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestValidationLoopHonorsIdleAndBackoffDeadlines(t *testing.T) {
	for _, mode := range []string{"removed", "settlement only", "quota", "error"} {
		t.Run(mode, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			s.options.PollInterval = time.Hour
			w.managed, w.ready = true, false
			w.validationNext = time.Now().Add(-time.Second)
			switch mode {
			case "removed":
				_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.BlockSourceBlocked{Source: queue.RepositoryAccess, Detail: "removed"}}, "")
				must(t, err)
			case "settlement only":
				w.settlementOnly = true
			default:
				w.Driver = nil
				w.validationNext = time.Now().Add(10 * time.Millisecond)
				w.rulesNextCheck = time.Now().Add(20 * time.Millisecond)
				w.Resolve = func(context.Context) (Target, error) {
					if mode == "quota" {
						return Target{}, &reportingQuota{time.Now().Add(time.Hour)}
					}
					return Target{}, errors.New("credentials offline")
				}
			}
			var logs bytes.Buffer
			s.options.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			wakeDone := make(chan struct{})
			go func() {
				defer close(wakeDone)
				if mode == "removed" || mode == "settlement only" {
					return
				}
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						select {
						case w.wake <- struct{}{}:
						default:
						}
					}
				}
			}()
			s.loop(ctx, w)
			<-wakeDone
			if cycles := strings.Count(logs.String(), "reconcile cycle started"); cycles != 1 {
				t.Fatalf("%s ran %d cycles in 300ms: %s", mode, cycles, logs.String())
			}
			if (mode == "removed" || mode == "settlement only") && !w.validationNext.IsZero() {
				t.Fatal("inactive validation deadline retained")
			}
		})
	}
}

func TestInactiveLifecyclesDoNotRefreshDiscovery(t *testing.T) {
	for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
		t.Run(lifecycle, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, f := managedFixture(t, repositoryConfig, Options{PollInterval: 20 * time.Millisecond, DiscoveryInterval: time.Hour})
				w := baseWorker(t, s, "main")
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				_, err = f.OpenPR(repo, 1, base, true)
				must(t, err)
				must(t, s.lifecycle(t.Context(), w, lifecycle, "lifecycle reason: "+lifecycle))
				w.validationNext = time.Now().Add(-time.Second)
				w.waiting = "unrelated configuration read failure"
				for len(s.refresh) > 0 {
					<-s.refresh
				}
				for len(w.wake) > 0 {
					<-w.wake
				}
				var sweeps atomic.Int32
				original := s.repositories[0].Branches
				s.repositories[0].Branches = func(ctx context.Context) ([]string, error) { sweeps.Add(1); return original(ctx) }
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- s.Run(ctx) }()
				// Wait for the initial cycle (including status publication) to finish.
				// A real deadline can cancel slow database I/O before it gets there.
				synctest.Wait()
				// Drive another turn explicitly, without racing a poll or cancellation.
				w.wake <- struct{}{}
				synctest.Wait()
				cancel()
				must(t, <-done)
				if sweeps.Load() != 0 || !w.validationNext.IsZero() {
					t.Fatalf("inactive queue drove discovery: sweeps=%d next=%v", sweeps.Load(), w.validationNext)
				}
				statuses := f.Statuses(repo, 1)
				if len(statuses) == 0 {
					t.Fatal("lifecycle status absent")
				}
				for _, check := range statuses {
					if check.Reason == "validation-pending" || !strings.Contains(check.Summary, lifecycle) {
						t.Fatalf("lifecycle hidden: %+v", statuses)
					}
				}
			})
		})
	}
}

func TestRefreshNotificationsHaveMinimumSweepInterval(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	var sweeps atomic.Int32
	original := d.installations[0].Repositories
	d.installations[0].Repositories = func(ctx context.Context) ([]Repository, error) { sweeps.Add(1); return original(ctx) }
	s.options.PollInterval = time.Hour
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	notificationsDone := make(chan struct{})
	go func() {
		defer close(notificationsDone)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = s.NotifyDiscovery(ctx, "7", "", "")
			}
		}
	}()
	must(t, s.Run(ctx))
	<-notificationsDone
	if sweeps.Load() > 1 {
		t.Fatalf("notification burst caused %d sweeps in 250ms", sweeps.Load())
	}
}

func TestWaitingPreservesPolicyAndDefersCommandsOnce(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig)
		must(t, s.cycle(t.Context(), w))
		before := w.prs[1]
		if before.HeadSHA == "" || !trusted(before) {
			t.Fatal("test requires complete trusted observation")
		}
		w.managed, w.ready = true, false
		w.waiting = "temporary validation failure"
		f.AddComment(repo, 1, "writer", "@openmerge queue")
		f.AddComment(repo, 1, w.BotLogin, "@openmerge queue")
		f.AddComment(repo, 1, "reader", "@openmerge queue")
		var logs bytes.Buffer
		s.options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		for range 3 {
			must(t, s.reportWaiting(t.Context(), w))
		}
		if !reflect.DeepEqual(w.prs[1], before) {
			t.Fatal("diagnostic reporting replaced complete policy data")
		}
		if count := strings.Count(logs.String(), "ignored @openmerge queue command"); count != 1 {
			t.Fatalf("ignored logs=%d: %s", count, logs.String())
		}
		must(t, s.commands(t.Context(), w, 1))
		comments := f.CommentRecords(repo, 1)
		receipt := fmt.Sprintf("comment/%d", comments[0].ID)
		raw, err := s.store.Observation(t.Context(), w.id, receipt)
		must(t, err)
		if len(raw) != 0 {
			t.Fatal("waiting consumed command receipt")
		}
		w.ready = true
		must(t, s.cycle(t.Context(), w))
		state := lifecycleState(t, s, w)
		if len(state.Entries) != 1 || !trusted(w.prs[1]) {
			t.Fatalf("same-tip recovery lost policy or queue command: %+v", state)
		}
		for range 2 {
			must(t, s.commands(t.Context(), w, 1))
		}
		events, err := s.store.EventsAfter(t.Context(), w.id, 0)
		must(t, err)
		requests := 0
		for _, event := range events {
			if e, ok := event.Event.(queue.EnqueueRequested); ok && e.PR == 1 {
				requests++
			}
		}
		if requests != 1 {
			t.Fatalf("pending command processed %d times", requests)
		}
		w.ready = false
		must(t, s.reportWaiting(t.Context(), w))
		if strings.Count(logs.String(), "ignored @openmerge queue command") != 1 {
			t.Fatal("honored command logged as ignored")
		}
	})
}

func TestDiscoveryClassicProtectionPermanentlyUnavailableIsReady(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	original := d.repos[0].Target
	d.repos[0].Target = func(base string) Target {
		target := original(base)
		target.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) {
			return nil, nil, fmt.Errorf("denied: %w", forge.ErrRulesUnavailable)
		}
		return target
	}
	restarted, err := NewDiscovery(t.Context(), s.store, d.source(), Options{})
	must(t, err)
	for range 3 {
		must(t, restarted.Refresh(t.Context()))
		w := restarted.workers()[0]
		if !w.ready || w.rulesFailures != 0 || w.rulesWaiting != "" || restarted.DiscoveryStatus().Repositories[0].State != "served" {
			t.Fatalf("unavailable protection gates discovery: %+v", restarted.DiscoveryStatus())
		}
		w.rulesNextCheck = time.Time{}
	}
}

func TestDiscoveryMixedBasesStatusDoesNotDependOnWorkerOrder(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	tip, err := d.f.PushBase(repo, "main", "multi base", map[string]string{".openmerge.yml": releaseConfig})
	must(t, err)
	must(t, d.f.PushRef(repo, "release/v1", tip))
	must(t, s.Refresh(t.Context()))
	must(t, d.f.DeleteRef(t.Context(), repo, "release/v1"))
	must(t, s.Refresh(t.Context()))
	for range 2 {
		if r := s.DiscoveryStatus().Repositories[0]; r.State != "served" {
			t.Fatalf("inactive base gated healthy main: %+v", r)
		}
		for i, j := 0, len(s.queues)-1; i < j; i, j = i+1, j-1 {
			s.queues[i], s.queues[j] = s.queues[j], s.queues[i]
		}
		must(t, s.Refresh(t.Context()))
	}
}

func TestBaseRegistrationFailureDoesNotGateOtherBases(t *testing.T) {
	db, conn := databaseConnection(t)
	_, d := newDiscoveryFixture(t)
	// Use the fixture's forge with a fresh store containing two conflicting main histories.
	key := store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}
	older, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	var newer store.QueueID
	err = conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','Owner/Repo','main','') RETURNING id`).Scan(&newer)
	must(t, err)
	for _, id := range []store.QueueID{older, newer} {
		_, err = db.Append(t.Context(), id, []queue.Event{queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
		must(t, err)
	}
	tip, err := d.f.PushBase(repo, "main", "multi base", map[string]string{".openmerge.yml": releaseConfig})
	must(t, err)
	must(t, d.f.PushRef(repo, "release/v1", tip))
	s, err := NewDiscovery(t.Context(), db, d.source(), Options{})
	must(t, err)
	if err = s.Refresh(t.Context()); err == nil {
		t.Fatal("conflicting registration accepted")
	}
	if !baseWorker(t, s, "release/v1").ready {
		t.Fatal("main registration failure skipped release base")
	}
	r := s.DiscoveryStatus().Repositories[0]
	if !strings.Contains(r.Detail, "case-variant") || strings.Contains(r.Detail, "Configuration could not be read") {
		t.Fatalf("wrong phase: %+v", r)
	}
}

func TestValidationPendingDoesNotIncreaseFailureBackoff(t *testing.T) {
	s, _, _, _ := fixture(t, repositoryConfig)
	for range 3 {
		s.refreshResult(fmt.Errorf("base validation: %w", &validationPending{"settlement pending"}))
	}
	if s.refreshFailures != 0 {
		t.Fatalf("pending increased failure count to %d", s.refreshFailures)
	}
	s.refreshResult(errors.Join(&validationPending{"pending"}, errors.New("actual failure")))
	if s.refreshFailures != 1 {
		t.Fatal("actual joined failure not counted")
	}
}

func TestRemovalReportsHeadsSeenOnlyWhileWaiting(t *testing.T) {
	s, d := newDiscoveryFixture(t)
	w := s.workers()[0]
	w.ready = false
	head, err := d.f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	_, err = d.f.OpenPR(repo, 1, head, true)
	must(t, err)
	must(t, s.reportWaiting(t.Context(), w))
	if len(w.prs) != 0 {
		t.Fatal("diagnostics populated policy observations")
	}
	d.repos = nil
	must(t, s.Refresh(t.Context()))
	statuses := d.f.Statuses(repo, 1)
	found := false
	for _, check := range statuses {
		if check.Reason == "repository-removed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("removal lost diagnostic head: %+v", statuses)
	}
}
