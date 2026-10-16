package queue

import (
	"slices"
	"strings"
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
		cleared := Apply(paused, RulesCleared{Basis: blocked.RulesRevision()})
		if len(paused.Blocks(testConfig)) != 2 || cleared.PauseReason(testConfig) == "" || cleared.PauseReason(testConfig) == "repository-rules" || len(actionsOf[BuildTestRef](Plan(testConfig, cleared, epoch))) != 0 {
			t.Fatalf("rules recovery cleared %T", pause)
		}
	}
	failed := blocked
	failed.buildRetry = buildRetryState{failures: 3, revision: 7}
	failed = Apply(failed, RulesCleared{Basis: blocked.RulesRevision()})
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

func TestRulesEventsRejectStaleReadBasis(t *testing.T) {
	state := Apply(State{}, RulesBlocked{Problems: []string{"original"}})
	basis := state.RulesRevision()
	// An unrelated event must not invalidate the pending rules read.
	state = Apply(state, BaseMoved{SHA: "base"})
	state = Apply(state, RulesBlocked{Problems: []string{"newer"}, Basis: basis})
	for _, stale := range []Event{
		RulesCleared{Basis: basis},
		RulesBlocked{Problems: []string{"older"}, Basis: basis},
		RulesCleared{Basis: basis + 2},
	} {
		state = Apply(state, stale)
		if state.RulesRevision() != basis+1 || !slices.Equal(state.RulesProblems(), []string{"newer"}) {
			t.Fatalf("stale %T changed rules state: revision=%d problems=%v", stale, state.RulesRevision(), state.RulesProblems())
		}
	}
	state = Apply(state, RulesCleared{Basis: state.RulesRevision()})
	if len(state.RulesProblems()) != 0 || state.RulesRevision() != basis+2 {
		t.Fatal("current recovery did not clear and advance the rules revision")
	}
	state = Apply(state, RulesBlocked{Problems: []string{"late block"}, Basis: basis + 1})
	if len(state.RulesProblems()) != 0 || state.RulesRevision() != basis+2 {
		t.Fatal("old block revived after recovery")
	}
	state = Apply(state, RulesCleared{Basis: state.RulesRevision()})
	if state.RulesRevision() != basis+3 {
		t.Fatal("an applied clear must advance even when no block remains")
	}
}

func TestRulesWireRequiresValidBasis(t *testing.T) {
	for _, kind := range []string{"rules_blocked", "rules_cleared"} {
		for _, basis := range []string{"", `,"basis":null`, `,"basis":-1`, `,"basis":"1"`, `,"basis":18446744073709551615`} {
			data := `{"version":1,"type":"` + kind + `","data":{"problems":["bad fence"]` + basis + `}}`
			if kind == "rules_cleared" {
				data = strings.Replace(data, `"problems":["bad fence"]`, "", 1)
				data = strings.Replace(data, "{,", "{", 1)
			}
			if _, err := DecodeEvent([]byte(data)); err == nil {
				t.Fatalf("accepted invalid rules event %s", data)
			}
		}
	}
	for _, event := range []Event{
		RulesBlocked{Problems: []string{"bad fence"}, Basis: ^uint64(0)},
		RulesCleared{Basis: ^uint64(0)},
	} {
		if _, _, err := EncodeEvent(event); err == nil {
			t.Fatalf("encoded overflowing revision for %T", event)
		}
	}
}

func TestRulesReportCannotReuseAcknowledgedPauseKey(t *testing.T) {
	state := State{BaseBranch: "main"}
	for _, event := range []Event{
		BaseMoved{SHA: "base"},
		PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		EnqueueRequested{PR: 1, At: epoch},
		PauseRequested{Reason: "operator", Detail: "investigating"},
	} {
		state = Apply(state, event)
	}
	previous := onlyAction[ReportStatus](t, Plan(testConfig, state, epoch))
	state = Apply(state, ActionCompleted{Key: previous.Key()})
	state = Apply(state, ResumeRequested{})
	// Make the rules counter equal the prior generic pause's log revision.
	for range 3 {
		state = Apply(state, RulesCleared{Basis: state.RulesRevision()})
	}
	state = Apply(state, RulesBlocked{Problems: []string{"bad fence"}, Basis: state.RulesRevision()})
	report := onlyAction[ReportStatus](t, Plan(testConfig, state, epoch))
	if report.Reason != "repository-rules" || report.Key() == previous.Key() {
		t.Fatal("earlier generic pause acknowledgement suppressed the rules report")
	}
}
