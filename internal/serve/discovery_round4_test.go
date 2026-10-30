package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestRunInstallationQuotaAllowsLaterValidationWake(t *testing.T) {
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	_, err := f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	r := managedRepository(f, repo.Path)
	original := r.Target
	r.Target = func(base string) Target {
		target := original(base)
		target.Key.Installation = "8"
		return target
	}
	var limitedCalls, eligibleCalls atomic.Int32
	until := time.Now().Add(time.Hour)
	d := Discovery{Installations: func(context.Context) ([]Installation, error) {
		return []Installation{
			{ID: "7", Repositories: func(context.Context) ([]Repository, error) {
				limitedCalls.Add(1)
				return nil, &reportingQuota{until}
			}},
			{ID: "8", Repositories: func(context.Context) ([]Repository, error) {
				eligibleCalls.Add(1)
				return []Repository{r}, nil
			}},
		}, nil
	}, Target: func(context.Context, store.QueueKey) (Target, error) { return r.Target("main"), nil }}
	s, err := NewDiscovery(t.Context(), database(t), d, Options{PollInterval: time.Hour, DiscoveryInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	if err := s.Refresh(t.Context()); err == nil {
		t.Fatal("expected installation-local quota")
	}
	w := s.workers()[0]
	if !w.ready {
		t.Fatal("healthy installation must be ready after the first sweep")
	}
	for len(s.refresh) > 0 {
		<-s.refresh
	}
	// The next worker cycle fails after the sweep found no pending validation.
	// Its notification must bypass the next scheduled discovery time.
	f.FailNext("DefaultBranch", errors.New("transient configuration metadata failure"))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	recovered := false
	for !recovered && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-tick.C:
			w.mu.Lock()
			recovered = eligibleCalls.Load() >= 2 && w.ready
			w.mu.Unlock()
		}
	}
	cancel()
	must(t, <-done)
	if !recovered || limitedCalls.Load() != 1 {
		t.Fatalf("later validation wake suppressed: recovered=%v limited=%d eligible=%d", recovered, limitedCalls.Load(), eligibleCalls.Load())
	}
}

func TestDeniedWaitingBoundaryCommentChecksPermissionOnce(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"waiting diagnostics", "commands"} {
		t.Run(mode, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, driver := fixture(t, repositoryConfig)
				must(t, s.cycle(t.Context(), w))
				w.managed, w.ready = true, false
				f.AddComment(repo, 1, "reader", "@openmerge queue")
				comment := f.CommentRecords(repo, 1)[0]
				before := driver.permissions
				for range 6 {
					if mode == "commands" {
						must(t, s.commands(t.Context(), w, 1))
					} else {
						must(t, s.reportWaiting(t.Context(), w))
					}
				}
				if got := driver.permissions - before; got != 1 {
					t.Fatalf("six denied waiting cycles made %d CanWrite reads, want 1", got)
				}
				receipt, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("comment/%d", comment.ID))
				must(t, err)
				if len(receipt) != 0 {
					t.Fatal("waiting consumed denied command")
				}
				f.SetCanWrite(repo, "reader", true)
				w.ready = true
				must(t, s.cycle(t.Context(), w))
				if driver.permissions-before != 2 || len(lifecycleState(t, s, w).Entries) != 1 {
					t.Fatal("readiness recovery did not freshly authorize previously denied command")
				}
			})
		})
	}
}

func TestFailedWaitingPermissionReadLeavesNoInspectionMarker(t *testing.T) {
	s, w, f, driver := fixture(t, repositoryConfig)
	w.managed, w.ready = true, false
	f.AddComment(repo, 1, "reader", "@openmerge queue")
	f.FailNext("CanWrite", errors.New("permissions unavailable"))
	if err := s.reportWaiting(t.Context(), w); err == nil {
		t.Fatal("permission failure swallowed")
	}
	comment := f.CommentRecords(repo, 1)[0]
	marker, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("ignored/comment/%d", comment.ID))
	must(t, err)
	if len(marker) != 0 {
		t.Fatal("failed permission read recorded a marker")
	}
	must(t, s.reportWaiting(t.Context(), w))
	must(t, s.reportWaiting(t.Context(), w))
	if driver.permissions != 2 {
		t.Fatalf("failed permission read not retried exactly once: %d", driver.permissions)
	}
}

type blockingFenceReader struct {
	forge.Git
	started chan struct{}
	release chan struct{}
}

func (g blockingFenceReader) ReadFence(ctx context.Context, repo forge.RepoRef, ref string) (string, string, string, error) {
	close(g.started)
	select {
	case <-ctx.Done():
		return "", "", "", ctx.Err()
	case <-g.release:
		return g.Git.ReadFence(ctx, repo, ref)
	}
}

func TestSlowFenceReconciliationDoesNotLockOtherRegistrations(t *testing.T) {
	db, conn := databaseConnection(t)
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	actual, _, _, err := f.ReadFence(t.Context(), repo, "openmerge/fence/main")
	must(t, err)
	for i, spelling := range []string{"Owner/Repo", "owner/repo"} {
		var id store.QueueID
		must(t, conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7',$1,'main','') RETURNING id`, spelling).Scan(&id))
		fence := "stale"
		if i == 1 {
			fence = actual
		}
		_, err = db.Append(t.Context(), id, []queue.Event{queue.FenceInitialized{Token: fence}}, "")
		must(t, err)
	}
	s, err := newServer(db, Options{})
	must(t, err)
	reader := blockingFenceReader{Git: f, started: make(chan struct{}), release: make(chan struct{})}
	target := managedRepository(f, repo.Path).Target("main")
	target.Git = reader
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.register(ctx, target); done <- err }()
	select {
	case <-reader.started:
	case <-ctx.Done():
		t.Fatal("fence reconciliation did not start")
	}
	_, unrelatedErr := s.register(ctx, managedRepository(f, "other/repo").Target("main"))
	close(reader.release)
	must(t, <-done)
	must(t, unrelatedErr)
	if len(s.workers()) != 2 {
		t.Fatal("registration lost a worker")
	}
}

func TestSingleVariantRegistrationDoesNotReadForgeFence(t *testing.T) {
	db := database(t)
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	target := managedRepository(f, repo.Path).Target("main")
	id, err := db.EnsureQueue(t.Context(), target.Key)
	must(t, err)
	_, err = db.Append(t.Context(), id, []queue.Event{queue.FenceInitialized{Token: "existing"}}, "")
	must(t, err)
	failure := errors.New("single-variant path must not read the fence")
	f.FailNext("ReadFence", failure)
	s, err := newServer(db, Options{})
	must(t, err)
	w, err := s.register(t.Context(), target)
	must(t, err)
	if w.id != id || lifecycleState(t, s, w).Fence != "existing" {
		t.Fatal("single-variant registration changed identity or fence")
	}
	_, _, _, err = f.ReadFence(t.Context(), repo, "openmerge/fence/main")
	if !errors.Is(err, failure) {
		t.Fatal("single-variant registration consumed a fence read")
	}
}
