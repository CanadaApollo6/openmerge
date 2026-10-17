package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestDiscoveryColdCanonicalFenceObservation(t *testing.T) {
	for _, mode := range []string{"owner", "read failure", "conflicting evidence"} {
		t.Run(mode, func(t *testing.T) {
			db, conn := databaseConnection(t)
			f := fake.New()
			f.Init(repo, "main", "openmerge/fence/main")
			_, err := f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
			must(t, err)
			actual, _, _, err := f.ReadFence(t.Context(), repo, "openmerge/fence/main")
			must(t, err)
			var stale, owner store.QueueID
			must(t, conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','Owner/Repo','main','') RETURNING id`).Scan(&stale))
			must(t, conn.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&owner))
			staleFence := "stale"
			if mode == "conflicting evidence" {
				staleFence = actual
			}
			_, err = db.Append(t.Context(), stale, []queue.Event{queue.FenceInitialized{Token: staleFence}}, "")
			must(t, err)
			_, err = db.Append(t.Context(), owner, []queue.Event{queue.FenceInitialized{Token: actual}}, "")
			must(t, err)
			_, err = db.Append(t.Context(), stale, []queue.Event{queue.LifecycleChanged{State: "active"}}, "")
			must(t, err)
			d := &discoveryFixture{f: f, repos: []Repository{managedRepository(f, repo.Path)}}
			d.installations = []Installation{{ID: "7", Repositories: func(context.Context) ([]Repository, error) { return d.repos, nil }}}
			s, err := NewDiscovery(t.Context(), db, d.source(), Options{})
			must(t, err)
			if mode == "read failure" {
				f.FailNext("ReadFence", errors.New("fence transport failure"))
			}
			err = s.Refresh(t.Context())
			var count int
			must(t, conn.QueryRow(t.Context(), `SELECT count(*) FROM canonical_queues`).Scan(&count))
			if mode != "owner" {
				if err == nil || !strings.Contains(err.Error(), "conflicting case-variant") || count != 0 {
					t.Fatalf("failed observation selected a history: err=%v cached=%d", err, count)
				}
				for _, w := range s.workers() {
					if w.ready {
						t.Fatal("unproven fence owner admitted")
					}
				}
				var events int
				must(t, conn.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&events))
				if events != 3 {
					t.Fatal("failed fence observation changed durable history")
				}
				if mode == "conflicting evidence" {
					return
				}
				// A subsequent complete observation can recover without intervention.
				err = s.Refresh(t.Context())
			}
			must(t, err)
			var selected store.QueueID
			must(t, conn.QueryRow(t.Context(), `SELECT queue_id FROM canonical_queues`).Scan(&selected))
			if selected != owner {
				t.Fatalf("later lifecycle event selected %d, want fence owner %d", selected, owner)
			}
		})
	}
}

func TestRunInstallationQuotaDoesNotDelayPendingValidation(t *testing.T) {
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	_, err := f.PushBase(repo, "main", "config", map[string]string{".openmerge.yml": repositoryConfig})
	must(t, err)
	var limitedCalls, eligibleCalls, checks atomic.Int32
	until := time.Now().Add(time.Hour)
	r := managedRepository(f, repo.Path)
	target := r.Target
	r.Target = func(base string) Target {
		t := target(base)
		t.Key.Installation = "8"
		t.CheckRules = func(context.Context) ([]string, []string, error) {
			if checks.Add(1) == 1 {
				return nil, nil, errors.New("rules temporarily unreadable")
			}
			return nil, nil, nil
		}
		return t
	}
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
	s, err := NewDiscovery(t.Context(), database(t), d, Options{PollInterval: time.Hour, DiscoveryInterval: time.Hour, RulesInterval: 50 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
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
			for _, r := range s.DiscoveryStatus().Repositories {
				if r.Installation == "8" && r.State == "served" {
					recovered = true
				}
			}
		}
	}
	cancel()
	must(t, <-done)
	if !recovered || limitedCalls.Load() != 1 || eligibleCalls.Load() < 2 {
		t.Fatalf("Run delayed eligible installation: recovered=%v limited=%d eligible=%d checks=%d", recovered, limitedCalls.Load(), eligibleCalls.Load(), checks.Load())
	}
	if s.refreshFailures != 0 {
		t.Fatal("installation quota plus pending validation counted as global failure")
	}
}

func TestRunAppQuotaRetainsGlobalDeadline(t *testing.T) {
	s, _ := newDiscoveryFixture(t)
	var calls atomic.Int32
	until := time.Now().Add(time.Hour)
	s.discovery.Installations = func(context.Context) ([]Installation, error) {
		calls.Add(1)
		return nil, &reportingQuota{until}
	}
	// Keep the existing worker pending with a much earlier validation wake.
	w := s.workers()[0]
	w.ready = false
	w.validationNext = time.Now().Add(10 * time.Millisecond)
	s.refreshNotBefore = time.Time{}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	must(t, s.Run(ctx))
	if calls.Load() != 1 || !s.refreshRetryAt.Equal(until) {
		t.Fatalf("App quota bypassed: calls=%d retry=%v", calls.Load(), s.refreshRetryAt)
	}
}

func TestWaitingBoundaryCommentChecksPermissionOnce(t *testing.T) {
	for _, mode := range []string{"waiting diagnostics", "commands"} {
		t.Run(mode, func(t *testing.T) {
			s, w, f, driver := fixture(t, repositoryConfig)
			must(t, s.cycle(t.Context(), w))
			w.managed, w.ready = true, false
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			before := driver.permissions
			for range 6 {
				if mode == "commands" {
					must(t, s.commands(t.Context(), w, 1))
				} else {
					must(t, s.reportWaiting(t.Context(), w))
				}
			}
			if got := driver.permissions - before; got != 1 {
				t.Fatalf("six waiting cycles made %d CanWrite reads, want 1", got)
			}
			comment := f.CommentRecords(repo, 1)[0]
			receipt, err := s.store.Observation(t.Context(), w.id, fmt.Sprintf("comment/%d", comment.ID))
			must(t, err)
			if len(receipt) != 0 {
				t.Fatal("waiting consumed pending command")
			}
			w.ready = true
			must(t, s.commands(t.Context(), w, 1))
			if driver.permissions-before != 2 || len(lifecycleState(t, s, w).Entries) != 1 {
				t.Fatal("readiness recovery did not freshly authorize pending command")
			}
		})
	}
}
