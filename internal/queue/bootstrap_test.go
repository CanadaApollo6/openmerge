package queue

import "testing"

func TestDeferredFenceBootstrapNeverReplacesAnyExistingToken(t *testing.T) {
	state := Apply(State{}, RulesBlocked{Problems: []string{"contents: write missing"}})
	state = Apply(state, RulesCleared{Basis: state.RulesRevision()})
	state = Apply(state, FenceInitialized{Token: "first"})
	if state.Fence != "first" {
		t.Fatal("deferred initialization failed")
	}
	state = Apply(state, FenceInitialized{Token: "replacement"})
	if state.Fence != "first" {
		t.Fatal("bootstrap replaced a recorded token")
	}
	seed := Apply(State{Fence: "deployed-token"}, FenceInitialized{Token: "replacement"})
	if seed.Fence != "deployed-token" {
		t.Fatal("bootstrap replaced a legacy immutable seed")
	}
	if _, _, err := EncodeEvent(FenceInitialized{}); err == nil {
		t.Fatal("empty token was persisted")
	}
	if _, err := DecodeEvent([]byte(`{"version":1,"type":"fence_initialized","data":{"token":""}}`)); err == nil {
		t.Fatal("empty token was decoded")
	}
}
