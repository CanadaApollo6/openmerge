package queue

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sourceFailure(source BlockSource) BlockSourceBlocked {
	e := BlockSourceBlocked{Source: source, Detail: "repair " + string(source)}
	if source == LandingNotOnBase || source == ActionPoison {
		e.Subject = "tested-or-action"
	}
	return e
}

func TestBlockSourcesOwnTheirRevisionsAndClears(t *testing.T) {
	for _, source := range blockSourceOrder {
		t.Run(string(source), func(t *testing.T) {
			original := State{}
			state := Apply(original, sourceFailure(source))
			basis := state.BlockSourceRevision(source)
			for _, other := range blockSourceOrder {
				if other != source {
					state = Apply(state, sourceFailure(other))
				}
			}
			state = Apply(state, RulesSourceBlocked{Source: Rulesets, Problems: []string{"rules"}})
			state = Apply(state, BlockSourceCleared{Source: source, Basis: basis})
			if original.SourceBlocked(source) || state.SourceBlocked(source) || state.BlockSourceRevision(source) != 2 || len(state.Blocks(testConfig)) != len(blockSourceOrder) {
				t.Fatalf("clear crossed sources or mutated input: %+v", state.Blocks(testConfig))
			}
			// An unchanged failure with an old basis invalidates an overlapping pass.
			state = Apply(state, sourceFailure(source))
			blocked := state
			for _, stale := range []uint64{basis, 2, 4} {
				state = Apply(state, BlockSourceCleared{Source: source, Basis: stale})
				if !state.SourceBlocked(source) || state.BlockSourceRevision(source) != 3 {
					t.Fatal("stale/future clear removed a failure")
				}
			}
			state = Apply(state, BlockSourceCleared{Source: source, Basis: 3})
			if !blocked.SourceBlocked(source) || state.SourceBlocked(source) {
				t.Fatal("clearing a snapshot mutated another snapshot")
			}
		})
	}
}

func TestEveryBlockSourceStopsStartsAndPreservesSettlement(t *testing.T) {
	state := State{BaseBranch: "main", Fence: "fence"}
	for _, event := range []Event{BaseMoved{SHA: "base"}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}} {
		state = Apply(state, event)
	}
	build := onlyAction[BuildTestRef](t, Plan(testConfig, state, epoch))
	for _, source := range blockSourceOrder {
		t.Run(string(source), func(t *testing.T) {
			blocked := Apply(state, sourceFailure(source))
			if len(actionsOf[BuildTestRef](Plan(testConfig, blocked, epoch))) != 0 || !DeferredByPause(testConfig, blocked, build, epoch) {
				t.Fatal("block did not defer a planned build")
			}
			testing := Apply(state, BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: build.PRs, At: epoch})
			testing = Apply(testing, TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: Merge, At: epoch})
			testing = Apply(testing, ChecksCompleted{TestedSHA: "tested", Success: true})
			land := onlyAction[Land](t, Plan(testConfig, testing, epoch))
			blocked = Apply(testing, sourceFailure(source))
			if len(actionsOf[Land](Plan(testConfig, blocked, epoch))) != 0 || !DeferredByPause(testConfig, blocked, land, epoch) {
				t.Fatal("block did not defer a planned landing")
			}
			landing := Apply(testing, LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: epoch})
			landing = Apply(landing, sourceFailure(source))
			if len(actionsOf[ResolveLanding](Plan(testConfig, landing, epoch.Add(time.Hour)))) != 1 {
				t.Fatal("source blocked landing settlement")
			}
		})
	}
}

func TestLegacyBlockFixturesRetainTheirFoldBehavior(t *testing.T) {
	read := func(name string) Event {
		t.Helper()
		data, err := os.ReadFile("testdata/wire/v1/event_" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		e, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	state := Apply(State{}, read("pause_requested"))
	pause := read("pause_requested").(PauseRequested)
	state = Apply(state, read("action_poisoned"))
	poison := state.actionPoison
	state = Apply(state, ActionPoisoned{Key: "ignored", Reason: "ignored"})
	if state.pause != pause || state.actionPoison != poison || state.Blocks(testConfig)[0].Source != Operator {
		t.Fatal("legacy poison idempotence or shared pause changed")
	}
	// The legacy shared pause still overwrites only the old shared slot.
	state = Apply(state, PauseRequested{Reason: "invalid-config", Detail: "old config failure"})
	if state.pause.Reason != "invalid-config" || state.actionPoison != poison {
		t.Fatal("legacy pause stopped overwriting the shared slot")
	}
	state = Apply(state, read("rules_blocked"))
	state = Apply(state, read("rules_source_blocked"))
	state.buildRetry = buildRetryState{failures: 3}
	state = Apply(state, read("resume_requested"))
	if state.pause != (PauseRequested{}) || state.actionPoison != (actionPoisonState{}) || state.buildRetry != (buildRetryState{}) || !state.hasRulesProblems() {
		t.Fatal("legacy resume no longer clears precisely the old pause, poison and retries")
	}
	state = Apply(state, read("rules_cleared")) // stale after the source observation
	if state.RulesRevision() != 2 || !state.hasRulesProblems() {
		t.Fatal("legacy rules basis changed")
	}
	state = Apply(state, RulesSourceCleared{Source: Rulesets, Basis: 2})
	if state.PauseReason(testConfig) != "" {
		t.Fatal("current rules clear failed")
	}
	// New explicit refresh compatibility clears the legacy pause and poison separately.
	state = Apply(state, read("pause_requested"))
	state = Apply(state, read("action_poisoned"))
	state = Apply(state, BlockSourceCleared{Source: Operator, Basis: state.BlockSourceRevision(Operator)})
	if state.pause.Reason != "" || state.actionPoison.key == "" {
		t.Fatal("operator clear crossed into legacy poison")
	}
	state = Apply(state, BlockSourceCleared{Source: ActionPoison, Basis: state.BlockSourceRevision(ActionPoison)})
	if state.PauseReason(testConfig) != "" {
		t.Fatal("legacy poison did not clear")
	}
	for _, source := range blockSourceOrder {
		state = Apply(state, sourceFailure(source))
	}
	before := state.Blocks(testConfig)
	state = Apply(state, ResumeRequested{})
	after := state.Blocks(testConfig)
	for i := range before {
		before[i].Revision, after[i].Revision = 0, 0
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("legacy resume cleared a new source")
	}
}

func TestBlockSourceWireValidation(t *testing.T) {
	for _, kind := range []string{"block_source_blocked", "block_source_cleared"} {
		for _, basis := range []string{"", `,"basis":null`, `,"basis":-1`, `,"basis":"1"`, `,"basis":18446744073709551615`} {
			data := `{"version":1,"type":"` + kind + `","data":{"source":"config","detail":"bad config","subject":""` + basis + `}}`
			if kind == "block_source_cleared" {
				data = strings.Replace(data, `,"detail":"bad config","subject":""`, "", 1)
			}
			if _, err := DecodeEvent([]byte(data)); err == nil {
				t.Fatalf("accepted %s", data)
			}
		}
	}
	for _, event := range []Event{
		BlockSourceBlocked{Source: ConfigSource}, BlockSourceBlocked{Source: "future", Detail: "blocked"},
		BlockSourceBlocked{Source: LandingNotOnBase, Detail: "missing SHA"},
		BlockSourceBlocked{Source: Operator, Detail: "operator", Subject: "unexpected"},
		BlockSourceCleared{Source: Operator, Basis: ^uint64(0)},
	} {
		if _, _, err := EncodeEvent(event); err == nil {
			t.Fatalf("encoded invalid %+v", event)
		}
	}
}

func TestSourceReportsCannotReuseAnotherSourcesAcknowledgement(t *testing.T) {
	state := State{BaseBranch: "main"}
	for _, e := range []Event{BaseMoved{SHA: "base"}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}, sourceFailure(Operator)} {
		state = Apply(state, e)
	}
	previous := onlyAction[ReportStatus](t, Plan(testConfig, state, epoch))
	state = Apply(state, ActionCompleted{Key: previous.Key()})
	state = Apply(state, sourceFailure(ConfigSource))
	report := onlyAction[ReportStatus](t, Plan(testConfig, state, epoch))
	if report.Key() == previous.Key() || report.Reason != "invalid-config" {
		t.Fatal("another source's acknowledgement suppressed the report")
	}
}
