package serve

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// The caller owns the fixture inside testSynctest. Wait drains each cycle
// (including real request/response DB I/O) before fake-clock advances.
func startIdleBackstop(t *testing.T, s *Server, w *worker) <-chan struct{} {
	t.Helper()
	s.options.WebhooksConfigured = true
	s.options.PollInterval = 50 * time.Millisecond
	s.options.BackstopInterval = 10 * time.Second
	s.webhooks.observe("7", repo.Path, time.Now())
	w.wake = make(chan struct{}, 1)
	s.slots = make(chan struct{}, s.options.Concurrency)
	polls := make(chan struct{}, 16)
	w.Driver = pollSignalDriver{Driver: w.Driver, polls: polls}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); s.loop(ctx, w) }()
	t.Cleanup(func() { cancel(); <-done })
	synctest.Wait()
	requireBackstopPoll(t, polls)
	time.Sleep(3 * s.options.PollInterval)
	synctest.Wait()
	select {
	case <-polls:
		t.Fatal("idle queue did not enter backstop")
	default:
	}
	return polls
}

func requireBackstopPoll(t *testing.T, polls <-chan struct{}) {
	t.Helper()
	select {
	case <-polls:
	default:
		t.Fatal("expected a completed reconcile")
	}
}

func requireBackstopTesting(t *testing.T, s *Server, w *worker) {
	t.Helper()
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing {
		t.Fatalf("expected testing batch: %+v", state.Active)
	}
}

func TestBackstopReplansInterruptedPeerEnqueue(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
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
		time.Sleep(s.options.PollInterval)
		synctest.Wait()
		requireBackstopTesting(t, s, w)
	})
}

func TestBackstopResumesPeerBuildingBatch(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
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
		time.Sleep(s.options.PollInterval)
		synctest.Wait()
		requireBackstopTesting(t, s, w)
	})
}

func TestBackstopRecomputesPeerPartialBatchDeadline(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
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
		// The peer cycle and state assertion consume no fake time. The waiting
		// server must notice the new history within exactly one poll interval.
		time.Sleep(s.options.PollInterval)
		synctest.Wait()
		requireBackstopPoll(t, polls)
		// Stay idle until the planner deadline, then require a build without any
		// further journal change or webhook to wake the server.
		time.Sleep(time.Until(at) - time.Nanosecond)
		synctest.Wait()
		state, _, err = s.store.Load(t.Context(), w.id)
		must(t, err)
		if state.Active != nil {
			t.Fatal("partial batch started before its deadline")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		requireBackstopPoll(t, polls)
		requireBackstopTesting(t, s, w)
	})
}
