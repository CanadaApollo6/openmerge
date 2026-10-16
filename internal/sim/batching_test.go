package sim

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func admitPR(t *testing.T, s *simulation, pr int) {
	t.Helper()
	for _, kind := range []eventKind{arrival, enqueue} {
		if err := s.deliver(scheduledEvent{kind: kind, pr: pr}); err != nil {
			t.Fatal(err)
		}
	}
}

func nextTestingBatch(t *testing.T, s *simulation) *queue.Batch {
	t.Helper()
	for range 1000 {
		if batch := s.state.Active; batch != nil && batch.Phase == queue.Testing {
			return batch
		}
		if done, err := s.step(); err != nil || done {
			t.Fatalf("no testing batch: done=%v err=%v", done, err)
		}
	}
	t.Fatal("no testing batch after 1000 steps")
	return nil
}

func TestStaleBatchTimeoutInSimulator(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.Planner.BatchMaxSize = 2, 2
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	old := nextTestingBatch(t, s).ID
	if err := s.pushExternalBase(); err != nil {
		t.Fatal(err)
	}
	current := nextTestingBatch(t, s).ID
	if current == old {
		t.Fatal("base move did not replace the batch")
	}
	// A timeout worker from the discarded batch delivers after the rebuild.
	s.record(queue.BatchFailed{BatchID: old, Reason: queue.ChecksTimeout})
	if r, err := s.run(); err != nil {
		t.Fatalf("simulator rejected mutation: %v; merged=%d", err, r.Merged)
	}
}

func TestChangedCompositionInSimulator(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.Planner.BatchMaxSize = 2, 8
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.now = s.start.Add(time.Second)
	admitPR(t, s, 1)
	if _, err := s.plan(); err != nil {
		t.Fatal(err)
	}
	// Publication precedes a second admission, without executing the old plan.
	admitPR(t, s, 2)
	s.scheduled = nil
	if r, err := s.run(); err != nil {
		t.Fatalf("simulator rejected mutation: %v; merged=%d", err, r.Merged)
	}
}

func TestBatchFormationWaitInSimulator(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.Planner.BatchMaxSize = 1, 8
	cfg.Planner.BatchMaxWait = 30 * time.Second
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Observe the real forge boundary independently of Plan and NextWake.
	deadline := s.start.Add(time.Second + cfg.Planner.BatchMaxWait)
	s.forge.OnTestRef(func(sha string, prs []int) {
		if s.now.Before(deadline) {
			s.violation = errors.New("partial batch built before its formation deadline")
			return
		}
		s.startCI(sha, prs)
	})
	if r, err := s.run(); err != nil {
		t.Fatalf("simulator rejected mutation: %v; merged=%d", err, r.Merged)
	}
}

func TestSimulatorRevalidatesBuildStart(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, change := range []string{"trust", "eligibility", "head", "close"} {
			name := "initial/"
			if retry {
				name = "retry/"
			}
			t.Run(name+change, func(t *testing.T) {
				cfg := smallConfig()
				cfg.PRs, cfg.Planner.BatchMaxSize = 2, 2
				s, err := newSimulation(cfg)
				if err != nil {
					t.Fatal(err)
				}
				s.now = s.start.Add(time.Second)
				admitPR(t, s, 1)
				admitPR(t, s, 2)
				build := queue.Plan(s.cfg.Planner, s.state, s.now)[0].(queue.BuildTestRef)
				if retry {
					if err := s.emit(build, queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: s.now}); err != nil {
						t.Fatal(err)
					}
				}
				observation := queue.PRObserved{PR: 2, HeadSHA: build.PRs[1].HeadSHA, Eligible: true, Trusted: true}
				switch change {
				case "trust":
					observation.Trusted = false
				case "eligibility":
					observation.Eligible = false
				case "head":
					observation.HeadSHA = "new-head"
				case "close":
					s.record(queue.PRClosed{PR: 2})
				}
				if change != "close" {
					s.record(observation)
				}
				before, calls := len(s.events), 0
				s.forge.OnTestRef(func(string, []int) { calls++ })
				err = s.executor.Execute(context.Background(), build, func(e queue.Event) error { return s.emit(build, e) })
				if !errors.Is(err, errStale) || calls != 0 || len(s.events) != before {
					t.Fatalf("stale build: error=%v calls=%d events=%d -> %d", err, calls, before, len(s.events))
				}
			})
		}
	}
}

func TestSimulatorRevalidatesLandingStart(t *testing.T) {
	s := onePR(t)
	land := readyLanding(t, s)
	s.record(queue.DequeueRequested{PR: 1})
	s.record(queue.Dequeued{PR: 1, AdmissionID: s.state.Entries[0].AdmissionID, Reason: queue.Requested})
	before := len(s.events)
	err := s.executor.Execute(context.Background(), land, func(e queue.Event) error { return s.emit(land, e) })
	tip, readErr := s.forge.ReadRef(context.Background(), s.repo, "main")
	if !errors.Is(err, errStale) || len(s.events) != before || readErr != nil || tip != s.rootSHA {
		t.Fatalf("stale landing: error=%v events=%d -> %d tip=%s read error=%v", err, before, len(s.events), tip, readErr)
	}
}
