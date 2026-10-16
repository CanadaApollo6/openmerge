package store

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFastForwardResolutionUsesDurableHeadDuringInflightLanding(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "marker-recovery")
	appendEvents(t, s, q, seedEvents()...)
	pins := []queue.BatchPR{{PR: 1, HeadSHA: "head"}}
	appendEvents(t, s, q, queue.FenceInitialized{Token: "fence"},
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: pins, At: epoch},
		queue.FastForwardBatchSelected{BatchID: "b1", PRs: pins},
		queue.FastForwardRefBuilt{BatchID: "b1", TestedSHA: "marker", HeadSHA: "head", At: epoch},
		queue.ChecksCompleted{TestedSHA: "marker", Success: true})
	cfg := queue.Config{MergeMethod: queue.FastForward, ResolveAfter: time.Second}
	_, err := s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 20, time.Hour, epoch)
	must(t, err)
	var land ClaimedAction
	for _, claim := range claims {
		if _, ok := claim.Action.(queue.Land); ok {
			land = claim
		}
	}
	if land.Key == "" {
		t.Fatal("missing land claim")
	}
	proceed, err := s.BeginConsequential(t.Context(), land, cfg, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("landing was not authorized")
	}
	must(t, s.Emit(t.Context(), land, cfg)(queue.LandingStarted{BatchID: "b1", AttemptID: "marker-attempt", FenceToken: "fence", At: epoch}))
	// The original land still holds its execution window; recovery must proceed
	// from the stored attempt even after the configured merge method changes.
	cfg.MergeMethod = queue.Squash
	now := epoch.Add(2 * time.Second)
	_, err = s.Plan(t.Context(), q, cfg, now)
	must(t, err)
	claims, err = s.Claim(t.Context(), 20, time.Minute, now)
	must(t, err)
	resolved := false
	for _, claim := range claims {
		resolve, ok := claim.Action.(queue.FastForwardResolveLanding)
		if !ok {
			continue
		}
		if resolve.HeadSHA != "head" || resolve.TestedSHA != "marker" {
			t.Fatalf("lost durable landing identity: %+v", resolve)
		}
		proceed, err := s.BeginConsequential(t.Context(), claim, cfg, now)
		must(t, err)
		if !proceed {
			t.Fatal("resolution blocked behind original landing")
		}
		must(t, s.Emit(t.Context(), claim, cfg)(queue.Landed{BatchID: "b1", NewBaseSHA: "head", NewFence: "landed-fence"}))
		resolved = true
	}
	if !resolved {
		t.Fatal("no fast-forward resolution claimed")
	}
}
