package queue

import (
	"slices"
	"testing"
	"time"
)

func TestExternalPauseStopsLandingButStillResolves(t *testing.T) {
	cfg := Config{MergeMethod: Squash, ResolveAfter: time.Second}
	state := State{BaseBranch: "main", Fence: "fence"}
	now := time.Now()
	for _, event := range []Event{BaseMoved{SHA: "base"}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: now}, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []BatchPR{{PR: 1, HeadSHA: "head"}}, At: now}, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: Squash, At: now}, ChecksCompleted{TestedSHA: "tested", Success: true}} {
		state = Apply(state, event)
	}
	pause := PauseRequested{Reason: "landing-not-on-base", Detail: "Restore tested commit on main"}
	paused := Apply(state, pause)
	for _, action := range Plan(cfg, paused, now) {
		if _, ok := action.(ReportStatus); !ok {
			t.Fatalf("paused action=%T", action)
		}
	}
	reported := paused
	for _, action := range Plan(cfg, reported, now) {
		reported = Apply(reported, ActionCompleted{Key: action.Key()})
	}
	if actions := Plan(cfg, reported, now); len(actions) != 0 {
		t.Fatalf("acknowledged pause report repeated: %+v", actions)
	}
	resumed := Apply(paused, ResumeRequested{})
	if !slices.ContainsFunc(Plan(cfg, resumed, now), func(a Action) bool { _, ok := a.(Land); return ok }) {
		t.Fatal("resume did not restore landing")
	}
	landing := Apply(state, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: now})
	landing = Apply(landing, pause)
	if !slices.ContainsFunc(Plan(cfg, landing, now.Add(2*time.Second)), func(a Action) bool { _, ok := a.(ResolveLanding); return ok }) {
		t.Fatal("pause stopped landing recovery")
	}
}
