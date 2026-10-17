package queue

import (
	"testing"
	"time"
)

func TestLifecycleBlocksAdmissionsAndStartsButKeepsResolution(t *testing.T) {
	for _, state := range []string{"dormant", "retired", "overlapping"} {
		t.Run(state, func(t *testing.T) {
			s := Apply(State{}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true})
			s = Apply(s, LifecycleChanged{State: state, Detail: "base unavailable"})
			s = Apply(s, EnqueueRequested{PR: 1, At: epoch})
			if len(s.Entries) != 0 {
				t.Fatal("admitted into inactive queue")
			}
			s = Apply(s, ResumeRequested{})
			if s.PauseReason(Config{}) != state {
				t.Fatal("operator resume bypassed lifecycle")
			}
			// A previously successful test cannot start a landing after retirement.
			s = Apply(s, LifecycleChanged{State: "active"})
			s = Apply(s, BaseMoved{SHA: "base"})
			s = Apply(s, EnqueueRequested{PR: 1, At: epoch})
			s = Apply(s, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch})
			s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: Merge, At: epoch})
			s = Apply(s, ChecksCompleted{TestedSHA: "tested", Success: true})
			s = Apply(s, LifecycleChanged{State: state, Detail: "base unavailable"})
			for _, a := range Plan(Config{}, s, epoch.Add(time.Hour)) {
				if _, ok := a.(Land); ok {
					t.Fatal("inactive queue started landing")
				}
			}
			s = Apply(s, LifecycleChanged{State: "active"})
			s = Apply(s, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: epoch})
			s = Apply(s, LifecycleChanged{State: state, Detail: "base unavailable"})
			found := false
			for _, a := range Plan(Config{}, s, epoch.Add(time.Hour)) {
				if IsLandingResolution(a) {
					found = true
				}
			}
			if !found {
				t.Fatal("inactive queue lost settlement")
			}
		})
	}
}
