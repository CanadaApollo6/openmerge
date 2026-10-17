package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func startIdleBackstop(t *testing.T, s *Server, w *worker) <-chan struct{} {
	t.Helper()
	s.options.WebhooksConfigured = true
	s.options.PollInterval = 50 * time.Millisecond
	s.options.BackstopInterval = 10 * time.Second
	s.webhooks.observe("7", repo.Path, time.Now())
	polls := make(chan struct{}, 16)
	w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx, w) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-polls:
	case <-time.After(3 * time.Second):
		t.Fatal("initial cycle missing")
	}
	select {
	case <-polls:
		t.Fatal("idle queue did not enter backstop")
	case <-time.After(3 * s.options.PollInterval):
	}
	return polls
}

func awaitTesting(t *testing.T, s *Server, w *worker, deadline time.Time) {
	t.Helper()
	for time.Now().Before(deadline) {
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if state.Active != nil && state.Active.Phase == queue.Testing {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("waiting server did not start testing shared work before deadline")
}

func TestBackstopReplansInterruptedPeerEnqueue(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	target := w.Target
	target.Driver = f
	other, err := New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	startIdleBackstop(t, s, w)
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	f.FailNext("Comment", errors.New("peer lost connection after enqueue commit"))
	if err := other.cycle(t.Context(), other.queues[0]); err == nil {
		t.Fatal("peer was not interrupted")
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 1 {
		t.Fatalf("expected durable enqueue without a batch: %+v", state)
	}
	awaitTesting(t, s, w, time.Now().Add(600*time.Millisecond))
}

func TestBackstopResumesPeerBuildingBatch(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	target := w.Target
	target.Driver = f
	other, err := New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	startIdleBackstop(t, s, w)
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	peer := other.queues[0]
	state, _, err := other.store.Load(t.Context(), peer.id)
	must(t, err)
	// The peer stopped after the durable batch marker, before build I/O.
	_, err = other.store.Append(t.Context(), peer.id, []queue.Event{
		queue.EnqueueRequested{PR: 1, At: time.Now()},
		queue.BatchStarted{BatchID: "b1", BaseSHA: state.BaseSHA, PRs: []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}, At: time.Now()},
	}, "")
	must(t, err)
	awaitTesting(t, s, w, time.Now().Add(600*time.Millisecond))
}

func TestBackstopRecomputesPeerPartialBatchDeadline(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig+"    batch: {max_size: 2, max_wait: 300ms}\n")
	target := w.Target
	target.Driver = f
	other, err := New(t.Context(), s.store, []Target{target}, s.options)
	must(t, err)
	polls := startIdleBackstop(t, s, w)
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, other.cycle(t.Context(), other.queues[0]))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 1 {
		t.Fatalf("expected partial batch: %+v", state)
	}
	at, ok := queue.NextWake(w.plannerConfig(s.options.PollInterval), state, time.Now())
	if !ok {
		t.Fatal("peer enqueue did not introduce a planner deadline")
	}
	select {
	case <-polls:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("shared history change did not trigger a normal reconcile")
	}
	// This also catches a reconcile that neglects to re-arm NextWake while idle.
	awaitTesting(t, s, w, at.Add(500*time.Millisecond))
}
