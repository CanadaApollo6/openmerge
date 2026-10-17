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
			s = Apply(s, LandingCleanupPolicyRecorded{BatchID: "b1", AttemptID: "attempt", DeleteHeadBranch: true})
			s = Apply(s, HeadCleanupRequested{BatchID: "b1", LandedSHA: "tested"})
			if hasCleanup(s) {
				t.Fatal("cleanup intent acted before Landed")
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

func TestHistoricalLandingsDoNotScheduleHeadCleanup(t *testing.T) {
	for _, method := range []MergeMethod{Merge, Squash, Rebase} {
		for _, intent := range []string{"absent", "matching-without-policy", "wrong-batch", "wrong-sha", "before-landing"} {
			t.Run(string(method)+"/"+intent, func(t *testing.T) {
				s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
				// Replay the original wire events, including more than one landing.
				for i, batch := range []string{"b1", "b2"} {
					pr := i + 1
					events := []Event{
						PRObserved{PR: pr, HeadSHA: "head", Eligible: true, Trusted: true},
						EnqueueRequested{PR: pr, At: epoch},
						BatchStarted{BatchID: batch, BaseSHA: s.BaseSHA, PRs: []BatchPR{{PR: pr, HeadSHA: "head"}}, At: epoch},
						TestRefBuilt{BatchID: batch, TestedSHA: "tested-" + batch, Method: method, At: epoch},
						ChecksCompleted{TestedSHA: "tested-" + batch, Success: true},
					}
					if intent == "before-landing" {
						events = append(events, HeadCleanupRequested{BatchID: batch, LandedSHA: "tested-" + batch})
					}
					events = append(events, LandingStarted{BatchID: batch, AttemptID: "attempt-" + batch, FenceToken: s.Fence, At: epoch})
					switch intent {
					case "matching-without-policy":
						events = append(events, HeadCleanupRequested{BatchID: batch, LandedSHA: "tested-" + batch})
					case "wrong-batch":
						events = append(events, HeadCleanupRequested{BatchID: "other", LandedSHA: "tested-" + batch})
					case "wrong-sha":
						events = append(events, HeadCleanupRequested{BatchID: batch, LandedSHA: "other"})
					}
					events = append(events, Landed{BatchID: batch, NewBaseSHA: "tested-" + batch, NewFence: "next-" + batch})
					for _, event := range events {
						_, data, err := EncodeEvent(event)
						if err != nil {
							t.Fatal(err)
						}
						replayed, err := DecodeEvent(data)
						if err != nil {
							t.Fatal(err)
						}
						s = Apply(s, replayed)
					}
				}
				if len(s.Entries) != 0 || s.Active != nil || s.BaseSHA != "tested-b2" {
					t.Fatalf("historical landings did not replay: %+v", s)
				}
				// Acknowledge closure too, so a hidden cleanup cannot evade the check.
				for _, action := range Plan(Config{MergeMethod: method}, s, epoch) {
					if _, ok := action.(MarkLanded); ok {
						s = Apply(s, ActionCompleted{Key: action.Key()})
					}
				}
				if deletions := actionsOf[DeleteHeadBranch](Plan(Config{MergeMethod: method}, s, epoch)); len(deletions) != 0 {
					t.Fatalf("historical replay scheduled head cleanup: %+v", deletions)
				}
			})
		}
	}
}
