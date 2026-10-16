package queue

import (
	"slices"
	"testing"
	"time"
)

func TestHeadCleanupRequiresConfirmedLandingAndCompletedClosure(t *testing.T) {
	for _, method := range []MergeMethod{Merge, Squash, Rebase} {
		t.Run(string(method), func(t *testing.T) {
			at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			cfg := Config{MergeMethod: method}
			s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
			for _, e := range []Event{PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: at}, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}, At: at}, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: method, At: at}, ChecksCompleted{TestedSHA: "tested", Success: true}, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: at}} {
				s = Apply(s, e)
			}
			cleanup := DeleteHeadBranch{PR: 1, HeadSHA: "head", LandedSHA: "tested"}
			hasCleanup := func(s State) bool {
				return slices.ContainsFunc(Plan(cfg, s, at), func(a Action) bool { return a.Key() == cleanup.Key() })
			}
			if hasCleanup(s) {
				t.Fatal("cleanup before landing confirmation")
			}
			s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"})
			if hasCleanup(s) != (method == Merge) {
				t.Fatal("cleanup did not wait for PR closure")
			}
			if method != Merge {
				s = Apply(s, ActionCompleted{Key: (MarkLanded(cleanup)).Key()})
			}
			if !hasCleanup(s) {
				t.Fatal("cleanup lost after closure")
			}
			s = Apply(s, ActionCompleted{Key: cleanup.Key()})
			if hasCleanup(s) {
				t.Fatal("completed cleanup repeated")
			}
			if cleanup.Key() == (DeleteHeadBranch{PR: 1, HeadSHA: "new", LandedSHA: "tested"}).Key() {
				t.Fatal("cleanup key lost head pin")
			}
		})
	}
}
