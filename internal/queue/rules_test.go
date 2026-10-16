package queue

import (
	"slices"
	"testing"
	"time"
)

func TestRulesBlockIsIndependentOfAllGenericPauses(t *testing.T) {
	state := State{BaseBranch: "main", Fence: "f0"}
	for _, event := range []Event{BaseMoved{SHA: "base"}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}} {
		state = Apply(state, event)
	}
	build := onlyAction[BuildTestRef](t, Plan(testConfig, state, epoch))
	problems := []string{"fence is unprotected"}
	blocked := Apply(state, RulesBlocked{Problems: problems})
	problems[0] = "caller mutation"
	copyProblems := blocked.RulesProblems()
	copyProblems[0] = "reader mutation"
	if blocked.RulesProblems()[0] != "fence is unprotected" || state.PauseReason(testConfig) != "" {
		t.Fatal("rules events or getters mutated another snapshot")
	}
	if len(actionsOf[BuildTestRef](Plan(testConfig, blocked, epoch))) != 0 || !DeferredByPause(testConfig, blocked, build, epoch) {
		t.Fatal("rules block did not defer existing build")
	}
	if resumed := Apply(blocked, ResumeRequested{}); resumed.PauseReason(testConfig) != "repository-rules" {
		t.Fatal("operator refresh cleared known rules block")
	}
	for _, pause := range []Event{ActionPoisoned{Key: "invalid", Reason: "invalid action"}, PauseRequested{Reason: "landing-not-on-base", Detail: "restore tested commit"}} {
		paused := Apply(blocked, pause)
		cleared := Apply(paused, RulesCleared{})
		if len(paused.Blocks(testConfig)) != 2 || cleared.PauseReason(testConfig) == "" || cleared.PauseReason(testConfig) == "repository-rules" || len(actionsOf[BuildTestRef](Plan(testConfig, cleared, epoch))) != 0 {
			t.Fatalf("rules recovery cleared %T", pause)
		}
	}
	failed := blocked
	failed.buildRetry = buildRetryState{failures: 3, revision: 7}
	failed = Apply(failed, RulesCleared{})
	if failed.PauseReason(testConfig) != "build-failed" {
		t.Fatal("rules recovery cleared build failure streak")
	}
	landing := Apply(landingState(), RulesBlocked{Problems: []string{"bad rules"}})
	if !slices.ContainsFunc(Plan(testConfig, landing, epoch.Add(testConfig.ResolveAfter+time.Second)), func(a Action) bool { _, ok := a.(ResolveLanding); return ok }) {
		t.Fatal("rules block suppressed fence resolution")
	}
}

func TestLegacyRulesPauseKeepsItsReplaySemantics(t *testing.T) {
	event, err := DecodeEvent([]byte(`{"version":1,"type":"pause_requested","data":{"reason":"repository-rules","detail":"legacy rules failure"}}`))
	if err != nil {
		t.Fatal(err)
	}
	state := Apply(State{}, event)
	state = Apply(state, RulesCleared{})
	if state.PauseReason(testConfig) != "repository-rules" || len(state.RulesProblems()) != 0 {
		t.Fatal("new event changed legacy generic pause semantics")
	}
	if Apply(state, ResumeRequested{}).PauseReason(testConfig) != "" {
		t.Fatal("legacy resume behavior changed")
	}
}
