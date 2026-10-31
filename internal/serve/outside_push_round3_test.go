package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Tests explicitly schedule audit passes independently of cycle/configuration.
func runOutsidePushAudit(t *testing.T, s *Server, w *worker) {
	t.Helper()
	history, _ := w.Git.(forge.OutsidePushHistory)
	s.options.OutsidePushHistory = func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error) { return history, nil }
	must(t, s.auditOutsidePushes(t.Context(), w.id, w.Key))
}

func assertFlaggedRewind(t *testing.T, s *Server, w *worker, landed, base string) {
	t.Helper()
	runOutsidePushAudit(t, s, w)
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := after.RecentOutsidePushes()
	if len(pushes) != 1 || pushes[0].PreviousSHA != landed || pushes[0].NewSHA != base || !pushes[0].Rewrite || !pushes[0].PossibleStaleObservation {
		t.Fatalf("expected one ambiguous observed rewind, own repair suppressed: %+v", pushes)
	}
}

func TestReviewRound3PeerSettlesAlreadyLandingAfterRefRead(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			s, w, f, before, req := outsidePushRound2Attempt(t, method, false)
			peer, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w.Git = &outsidePushRefHook{Forge: f, afterRead: func() {
				_, err := f.Land(t.Context(), repo, req, forge.PushOptions{})
				must(t, err)
				held, err := peer.settle(t.Context(), peer.queues[0])
				must(t, err)
				if held {
					t.Fatal("peer failed settlement")
				}
			}}
			must(t, s.configuration(t.Context(), w))
			must(t, s.cycle(t.Context(), w))
			assertFlaggedRewind(t, s, w, before.Active.LandingCommit(), before.BaseSHA)
		})
	}
}

// A real rollback/restore is journal-identical to the peer's stale-read repair.
// The audit must expose precisely the same uncertainty in both cases.
func TestOutsidePushRealRollbackAndRestoreHasSameFlag(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
				s, w, f, before, req := outsidePushRound2Attempt(t, method, false)
				_, err := f.Land(t.Context(), repo, req, forge.PushOptions{})
				must(t, err)
				held, err := s.settle(t.Context(), w)
				must(t, err)
				if held {
					t.Fatal("landing did not settle")
				}
				runOutsidePushAudit(t, s, w) // persist the landing receipt before the rewind
				must(t, f.SetRef(repo, "main", before.BaseSHA))
				must(t, s.configuration(t.Context(), w))
				must(t, f.SetRef(repo, "main", before.Active.LandingCommit()))
				must(t, s.configuration(t.Context(), w))
				restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
				must(t, err)
				assertFlaggedRewind(t, restarted, restarted.queues[0], before.Active.LandingCommit(), before.BaseSHA)
			})
		})
	}
}

func TestOutsidePushOnlyExactPreLandingRewindIsFlagged(t *testing.T) {
	t.Parallel()
	testSynctest(t, func(t *testing.T) {
		s, w, f, before, req := outsidePushRound2Attempt(t, "merge", false)
		_, err := f.Land(t.Context(), repo, req, forge.PushOptions{})
		must(t, err)
		held, err := s.settle(t.Context(), w)
		must(t, err)
		if held {
			t.Fatal("landing did not settle")
		}
		runOutsidePushAudit(t, s, w)
		must(t, f.SetRef(repo, "main", before.BaseSHA))
		must(t, s.configuration(t.Context(), w))
		next, err := f.PushBase(repo, "main", "continue after rewind", nil)
		must(t, err)
		must(t, s.configuration(t.Context(), w))
		must(t, f.SetRef(repo, "main", before.BaseSHA))
		must(t, s.configuration(t.Context(), w))
		runOutsidePushAudit(t, s, w)
		after, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		pushes := after.RecentOutsidePushes()
		if len(pushes) != 3 || !pushes[0].PossibleStaleObservation || pushes[1].PossibleStaleObservation || pushes[2].PossibleStaleObservation || pushes[1].NewSHA != next || !pushes[2].Rewrite {
			t.Fatalf("flag applied beyond exact landing-base rewind: %+v", pushes)
		}
	})
}

type reviewRound3BlockingHistory struct {
	*fake.Forge
	entered chan struct{}
	release chan struct{}
}

func (g *reviewRound3BlockingHistory) CommitsBetween(ctx context.Context, r forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	return g.Forge.CommitsBetween(ctx, r, base, head, limit)
}

func TestReviewRound3AuditHoldsWorkerAgainstNextSettlement(t *testing.T) {
	t.Parallel()
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	// An older durable outside move is ready for enrichment while a newer
	// landing is unresolved. A stuck audit must not own the worker or its slot.
	_, err := f.PushBase(repo, "main", "earlier outside push", nil)
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	before, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	b := before.Active
	if b == nil || b.Phase != queue.Testing {
		t.Fatal("not testing")
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "round3", FenceToken: before.Fence, At: time.Now().Add(-time.Hour)}}, "")
	must(t, err)
	_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: before.BaseSHA, TestedSHA: b.TestedSHA, Fence: "openmerge/fence/main", ExpectedFence: before.Fence, NextFenceID: "land/round3", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}}, forge.PushOptions{})
	must(t, err)
	must(t, f.SetRef(repo, "main", before.BaseSHA))
	var absent *executor.ErrLandingNotOnBase
	if err := s.cycle(t.Context(), w); !errors.As(err, &absent) {
		t.Fatalf("main's settlement safety result changed: %v", err)
	}
	history := &reviewRound3BlockingHistory{Forge: f, entered: make(chan struct{}, 1), release: make(chan struct{})}
	s.options.OutsidePushHistory = func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error) { return history, nil }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { s.outsidePushLoop(ctx); close(done) }()
	defer func() { cancel(); close(history.release); <-done }()
	select {
	case <-history.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("audit never reached history barrier")
	}
	if !w.mu.TryLock() {
		t.Fatal("stalled audit holds worker mutex")
	}
	w.mu.Unlock()
	// Use the real worker loop with exactly one execution slot. Restore the
	// landing and wake a cycle while audit I/O remains blocked.
	s.slots = make(chan struct{}, 1)
	must(t, f.SetRef(repo, "main", b.LandingCommit()))
	workerDone := make(chan struct{})
	go func() { s.loop(ctx, w); close(workerDone) }()
	defer func() { cancel(); <-workerDone }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, _, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if state.Active == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stuck audit delayed now-resolvable landing settlement")
		}
		time.Sleep(time.Millisecond)
	}
	// A following cycle still completes before enrichment is released.
	next := make(chan error, 1)
	go func() { next <- s.cycle(ctx, w) }()
	select {
	case err := <-next:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("stuck audit delayed the next cycle")
	}
	select {
	case <-history.release:
		t.Fatal("history unexpectedly released")
	default:
	}
}

func TestOutsidePushTwoServersNoLiveRefReadAndNoLostNewerTip(t *testing.T) {
	for _, olderFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "older-first", false: "newer-first"}[olderFirst], func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			must(t, s.cycle(t.Context(), w))
			runOutsidePushAudit(t, s, w)
			first, err := f.PushBase(repo, "main", "B", nil)
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			peer, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			olderHistory := &reviewRound3BlockingHistory{Forge: f, entered: make(chan struct{}, 1), release: make(chan struct{})}
			newerHistory := &reviewRound3BlockingHistory{Forge: f, entered: make(chan struct{}, 1), release: make(chan struct{})}
			s.options.OutsidePushHistory = func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error) { return olderHistory, nil }
			peer.options.OutsidePushHistory = func(context.Context, store.QueueKey) (forge.OutsidePushHistory, error) { return newerHistory, nil }
			oldDone := make(chan error, 1)
			go func() { oldDone <- s.auditOutsidePushes(t.Context(), w.id, w.Key) }()
			select {
			case <-olderHistory.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("older audit did not start")
			}
			next, err := f.PushBase(repo, "main", "C", nil)
			must(t, err)
			must(t, peer.configuration(t.Context(), peer.queues[0]))
			newDone := make(chan error, 1)
			go func() { newDone <- peer.auditOutsidePushes(t.Context(), w.id, w.Key) }()
			select {
			case <-newerHistory.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("newer audit did not start")
			}
			if olderFirst {
				close(olderHistory.release)
				must(t, <-oldDone)
				close(newerHistory.release)
				must(t, <-newDone)
			} else {
				close(newerHistory.release)
				must(t, <-newDone)
				close(olderHistory.release)
				must(t, <-oldDone)
			}
			// Live ref changes without a BaseMoved cannot manufacture audit input.
			must(t, f.SetRef(repo, "main", first))
			runOutsidePushAudit(t, s, w)
			after, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			pushes := after.RecentOutsidePushes()
			if len(pushes) != 2 || pushes[0].NewSHA != first || pushes[1].NewSHA != next {
				t.Fatalf("lost newer observation or invented live-ref rollback: %+v", pushes)
			}
		})
	}
}
