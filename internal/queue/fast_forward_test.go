package queue

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestFastForwardSelectionReplayAndStaleEvents(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	cfg := Config{MergeMethod: FastForward, BatchMaxSize: 3}
	s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	pins := []BatchPR{{PR: 1, HeadSHA: "one"}, {PR: 2, HeadSHA: "two"}, {PR: 3, HeadSHA: "three"}}
	for _, pin := range pins {
		s = Apply(s, PRObserved{PR: pin.PR, HeadSHA: pin.HeadSHA, Eligible: true, Trusted: true})
		s = Apply(s, EnqueueRequested{PR: pin.PR, At: at})
	}
	s = Apply(s, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: pins, At: at})
	for _, event := range []Event{
		FastForwardBatchSelected{BatchID: "stale", PRs: pins[:1]},
		FastForwardBatchSelected{BatchID: "b1", PRs: pins[1:]},
		FastForwardBatchSelected{BatchID: "b1", PRs: nil},
	} {
		after := Apply(s, event)
		if !reflect.DeepEqual(after.Active, s.Active) || !reflect.DeepEqual(after.pending, s.pending) {
			t.Fatalf("invalid selection changed batch: %+v", event)
		}
	}
	selected := FastForwardBatchSelected{BatchID: "b1", PRs: pins[:2]}
	before := s
	s = Apply(s, selected)
	if !slices.Equal(before.Active.PRs, pins) {
		t.Fatal("Apply mutated its input")
	}
	s = Apply(s, selected) // A crash/retry cannot duplicate the remainder.
	if len(s.pending) != 1 || !slices.Equal(s.pending[0].prs, pins[2:]) {
		t.Fatal("selection lost/repeated remainder")
	}
	s = Apply(s, FastForwardRefBuilt{BatchID: "b1", TestedSHA: "three", At: at})
	if s.Active.Phase != Building {
		t.Fatal("unselected head became tested")
	}
	s = Apply(s, FastForwardRefBuilt{BatchID: "b1", TestedSHA: "two", At: at})
	s = Apply(s, FastForwardBlocked{BatchID: "b1"})
	if s.Active.Phase != Testing || s.PendingDequeue(1) != "" {
		t.Fatal("late build rejection revoked test")
	}
	s = Apply(s, BaseMoved{SHA: "new-base"})
	s = Apply(s, FastForwardBlocked{BatchID: "b1"})
	s = Apply(s, FastForwardRefBuilt{BatchID: "b1", TestedSHA: "two", At: at})
	if s.Active != nil {
		t.Fatal("stale result restored canceled batch")
	}
	build := Plan(cfg, s, at)
	if !slices.ContainsFunc(build, func(a Action) bool {
		b, ok := a.(BuildTestRef)
		return ok && slices.Equal(b.PRs, pins[:2]) && b.BaseSHA == "new-base"
	}) {
		t.Fatal("base move lost selected retry group")
	}
}

func TestFastForwardMayRetestUnchangedHeadAfterFailure(t *testing.T) {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	pins := []BatchPR{{PR: 1, HeadSHA: "head"}}
	s = Apply(s, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true})
	s = Apply(s, EnqueueRequested{PR: 1, At: at})
	for _, id := range []string{"b1", "b2"} {
		s = Apply(s, BatchStarted{BatchID: id, BaseSHA: "base", PRs: pins, At: at})
		s = Apply(s, FastForwardBatchSelected{BatchID: id, PRs: pins})
		s = Apply(s, FastForwardRefBuilt{BatchID: id, TestedSHA: "head", At: at})
		if s.Active == nil || s.Active.Phase != Testing {
			t.Fatal("unchanged head rejected on retry")
		}
		s = Apply(s, LandingStarted{BatchID: id, AttemptID: id, FenceToken: s.Fence, At: at})
		s = Apply(s, LandingAbandoned{BatchID: id, NewFence: id})
	}
}
