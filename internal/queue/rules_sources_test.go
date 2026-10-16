package queue

import (
	"slices"
	"strings"
	"testing"
)

func TestRulesSourcesKeepIndependentRevisionsAndBlocks(t *testing.T) {
	for _, source := range rulesSourceOrder {
		t.Run(string(source), func(t *testing.T) {
			original := State{}
			problems := []string{"original"}
			state := Apply(original, RulesSourceBlocked{Source: source, Problems: problems})
			problems[0] = "mutated input"
			findings := state.RulesSources()
			findings[0].Problems[0] = "mutated output"
			if original.hasRulesProblems() || state.RulesSourceProblems(source)[0] != "original" {
				t.Fatal("source state escaped its snapshot")
			}
			basis := state.RulesSourceRevision(source)
			// Concurrent observations of every other source do not stale this pass.
			for _, other := range rulesSourceOrder {
				if other != source {
					state = Apply(state, RulesSourceBlocked{Source: other, Problems: []string{string(other)}})
				}
			}
			state = Apply(state, RulesSourceCleared{Source: source, Basis: basis})
			if len(state.RulesSourceProblems(source)) != 0 || len(state.RulesProblems()) != 2 || state.RulesSourceRevision(source) != 2 {
				t.Fatal("source pass cleared another source or was invalidated by it")
			}
			// Stale failures always apply. Stale/future clears cannot remove them.
			state = Apply(state, RulesSourceBlocked{Source: source, Problems: []string{"newer"}, Basis: 0})
			for _, stale := range []uint64{basis, 4} {
				state = Apply(state, RulesSourceCleared{Source: source, Basis: stale})
				if !slices.Equal(state.RulesSourceProblems(source), []string{"newer"}) || state.RulesSourceRevision(source) != 3 {
					t.Fatal("stale passing read cleared a newer block")
				}
			}
			for _, current := range state.RulesSources() {
				state = Apply(state, RulesSourceCleared{Source: current.Source, Basis: current.Revision})
			}
			if state.PauseReason(testConfig) != "" {
				t.Fatal("all sources passed but queue stayed blocked")
			}
		})
	}
}

func TestEveryRulesSourceDefersPlannedWork(t *testing.T) {
	state := State{BaseBranch: "main", Fence: "f0"}
	for _, event := range []Event{BaseMoved{SHA: "base"}, PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}} {
		state = Apply(state, event)
	}
	build := onlyAction[BuildTestRef](t, Plan(testConfig, state, epoch))
	for _, source := range rulesSourceOrder {
		state = Apply(state, RulesSourceBlocked{Source: source, Problems: []string{"blocked"}})
	}
	for _, source := range rulesSourceOrder {
		if len(actionsOf[BuildTestRef](Plan(testConfig, state, epoch))) != 0 || !DeferredByPause(testConfig, state, build, epoch) {
			t.Fatal("a source block did not defer work")
		}
		state = Apply(state, RulesSourceCleared{Source: source, Basis: state.RulesSourceRevision(source)})
	}
	if len(actionsOf[BuildTestRef](Plan(testConfig, state, epoch))) != 1 {
		t.Fatal("clearing all sources did not restore the plan")
	}
}

func TestLegacyRulesEventsShareOnlyRulesetsState(t *testing.T) {
	state := Apply(State{}, RulesBlocked{Problems: []string{"legacy block"}})
	state = Apply(state, RulesSourceBlocked{Source: ClassicProtection, Problems: []string{"classic block"}})
	state = Apply(state, RulesCleared{Basis: 1})
	if state.RulesRevision() != 2 || len(state.RulesSourceProblems(Rulesets)) != 0 || !slices.Equal(state.RulesSourceProblems(ClassicProtection), []string{"classic block"}) {
		t.Fatal("legacy clear crossed source boundaries")
	}
	state = Apply(state, RulesSourceBlocked{Source: Rulesets, Problems: []string{"new block"}})
	state = Apply(state, RulesCleared{Basis: 2})
	if !slices.Equal(state.RulesSourceProblems(Rulesets), []string{"new block"}) {
		t.Fatal("legacy clear ignored a newer source block")
	}
	state = Apply(state, RulesCleared{Basis: 3})
	if state.RulesRevision() != 4 || !state.hasRulesProblems() {
		t.Fatal("current legacy clear did not preserve classic block")
	}
}

func TestRulesSourceWireValidation(t *testing.T) {
	for _, kind := range []string{"rules_source_blocked", "rules_source_cleared"} {
		for _, source := range []string{"", "unknown", "RULESETS"} {
			data := `{"version":1,"type":"` + kind + `","data":{"source":"` + source + `","problems":["blocked"],"basis":0}}`
			if kind == "rules_source_cleared" {
				data = strings.Replace(data, `,"problems":["blocked"]`, "", 1)
			}
			if _, err := DecodeEvent([]byte(data)); err == nil {
				t.Fatalf("accepted invalid source: %s", data)
			}
		}
		for _, basis := range []string{"", `,"basis":null`, `,"basis":-1`, `,"basis":"1"`, `,"basis":18446744073709551615`} {
			data := `{"version":1,"type":"` + kind + `","data":{"source":"rulesets","problems":["blocked"]` + basis + `}}`
			if kind == "rules_source_cleared" {
				data = strings.Replace(data, `,"problems":["blocked"]`, "", 1)
			}
			if _, err := DecodeEvent([]byte(data)); err == nil {
				t.Fatalf("accepted invalid basis: %s", data)
			}
		}
	}
	for _, event := range []Event{
		RulesSourceBlocked{Source: Rulesets},
		RulesSourceBlocked{Source: "future", Problems: []string{"blocked"}},
		RulesSourceCleared{Source: Rulesets, Basis: ^uint64(0)},
	} {
		if _, _, err := EncodeEvent(event); err == nil {
			t.Fatalf("encoded invalid event: %+v", event)
		}
	}
}
