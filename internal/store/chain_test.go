package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func chainStart(a queue.BuildChainedTestRef) queue.ChainedBatchStarted {
	return queue.ChainedBatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: a.PRs, At: epoch, ParentBatchID: a.ParentBatchID, Position: a.Position}
}
func TestChainAdmissionBothDurableGates(t *testing.T) {
	for _, window := range []string{"before-begin", "before-append", "repeat-start"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			appendEvents(t, s, q, seedEvents()...)
			cfg := config
			cfg.SpeculationMaxDepth = 4
			_, err := s.Plan(t.Context(), q, cfg, epoch)
			must(t, err)
			claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
			must(t, err)
			a := claims[0].Action.(queue.BuildChainedTestRef)
			if window == "before-begin" {
				appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
			}
			proceed, err := s.BeginConsequential(t.Context(), claims[0], cfg, epoch)
			must(t, err)
			if window == "before-begin" {
				if proceed {
					t.Fatal("stale chain admission passed first gate")
				}
				return
			}
			if !proceed {
				t.Fatal("current chain build declined")
			}
			emit := s.Emit(t.Context(), claims[0], cfg)
			if window == "repeat-start" {
				must(t, emit(chainStart(a)))
				must(t, emit(chainStart(a)))
				state, _, err := s.Load(t.Context(), q)
				must(t, err)
				if len(state.Batches()) != 1 {
					t.Fatal("repeat admission duplicated batch")
				}
				return
			}
			appendEvents(t, s, q, queue.DequeueRequested{PR: 1})
			if err := emit(chainStart(a)); !errors.Is(err, ErrStale) {
				t.Fatalf("stale admission append=%v", err)
			}
		})
	}
}
func TestChainSuccessorLandingRevocationBothGates(t *testing.T) {
	for _, window := range []string{"before-begin", "before-append"} {
		t.Run(window, func(t *testing.T) {
			s := newStore(t)
			q := ensureQueue(t, s, "")
			cfg := config
			cfg.SpeculationMaxDepth = 4
			appendEvents(t, s, q, queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "h1", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch}, queue.PRObserved{PR: 2, HeadSHA: "h2", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: epoch}, queue.ChainedBatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "h1"}}, At: epoch, Position: 1}, queue.ChainedTestRefBuilt{BatchID: "b1", TestedSHA: "t1", LandingSHA: "t1", Method: queue.Squash, At: epoch, Position: 1}, queue.ChainedBatchStarted{BatchID: "b2", BaseSHA: "t1", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "h2"}}, At: epoch, ParentBatchID: "b1", Position: 2}, queue.ChainedTestRefBuilt{BatchID: "b2", TestedSHA: "t2", LandingSHA: "t2", Method: queue.Squash, At: epoch, ParentBatchID: "b1", Position: 2}, queue.ChecksCompleted{TestedSHA: "t1", Success: true}, queue.ChecksCompleted{TestedSHA: "t2", Success: true}, queue.LandingStarted{BatchID: "b1", AttemptID: "first", FenceToken: "fence", At: epoch}, queue.Landed{BatchID: "b1", NewBaseSHA: "t1", NewFence: "fence2"})
			plans, err := s.Plan(t.Context(), q, cfg, epoch)
			must(t, err)
			// Complete harmless recorded effects so this test claims only the Land.
			for _, p := range plans {
				if _, ok := p.Action.(queue.Land); !ok {
					appendEvents(t, s, q, queue.ActionCompleted{Key: p.Key})
				}
			}
			// Cancel already planned effects through the ordinary consequential check.
			var claim ClaimedAction
			for range 10 {
				claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
				must(t, err)
				if len(claims) == 0 {
					t.Fatal("no successor landing")
				}
				if _, ok := claims[0].Action.(queue.Land); ok {
					claim = claims[0]
					break
				}
				must(t, s.Complete(t.Context(), claims[0], Done))
			}
			land := claim.Action.(queue.Land)
			reopen := func() {
				appendEvents(t, s, q, queue.ChecksReopened{TestedSHA: "t2", At: epoch}, queue.ChecksCompleted{TestedSHA: "t2", Success: true})
			}
			if window == "before-begin" {
				reopen()
			}
			proceed, err := s.BeginConsequential(t.Context(), claim, cfg, epoch)
			must(t, err)
			if window == "before-begin" {
				if proceed {
					t.Fatal("old successor generation passed first gate")
				}
				return
			}
			if !proceed {
				t.Fatal("current successor rejected")
			}
			reopen()
			err = s.Emit(t.Context(), claim, cfg)(queue.LandingStarted{BatchID: land.BatchID, AttemptID: "second", FenceToken: land.FenceToken, At: epoch})
			if !errors.Is(err, ErrStale) {
				t.Fatalf("old successor generation passed append gate: %v", err)
			}
		})
	}
}
