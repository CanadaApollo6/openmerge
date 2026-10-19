package queue

import (
	"reflect"
	"testing"
	"time"
)

func TestRebaseCompositionReplayAndResume(t *testing.T) {
	for _, depth := range []int{1, 4} {
		cfg := Config{MergeMethod: Rebase, BatchMaxSize: 4, SpeculationMaxDepth: depth}
		now := time.Now()
		state := State{BaseBranch: "main"}
		state = Apply(state, BaseMoved{SHA: "base"})
		prs := []BatchPR{{1, "h1"}, {2, "h2"}, {3, "h3"}, {4, "h4"}}
		for _, p := range prs {
			state = Apply(state, PRObserved{PR: p.PR, HeadSHA: p.HeadSHA, Eligible: true, Trusted: true})
			state = Apply(state, EnqueueRequested{PR: p.PR, At: now})
		}
		var id string
		for _, a := range Plan(cfg, state, now) {
			switch a := a.(type) {
			case BuildTestRef:
				id = a.BatchID
				state = Apply(state, BatchStarted{BatchID: id, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now})
			case BuildChainedTestRef:
				id = a.BatchID
				state = Apply(state, ChainedBatchStarted{BatchID: id, BaseSHA: a.BaseSHA, PRs: a.PRs, At: now, ParentBatchID: a.ParentBatchID, Position: a.Position})
			}
		}
		event := RebaseCompositionObserved{BatchID: id, PRs: prs, Counts: []int{5, 3, 2, 1}, Limit: 4}
		state = Apply(state, event)
		if state.Active == nil || !reflect.DeepEqual(state.Active.PRs, prs[1:2]) {
			t.Fatalf("selection: %+v", state.Active)
		}
		// A crash after selection resumes the supported subset, never the old
		// oversized composition. Duplicate/stale selection must not defer twice.
		restored := snapshotRoundTrip(t, cfg, state, now)
		restored = Apply(restored, event)
		if !reflect.DeepEqual(state.Batches(), restored.Batches()) || !reflect.DeepEqual(Plan(cfg, state, now), Plan(cfg, restored, now)) {
			t.Fatal("duplicate changed composition")
		}
		if len(restored.pending) != 1 || !reflect.DeepEqual(restored.pending[0].prs, prs[2:]) {
			t.Fatal("deferred suffix lost")
		}
		for _, a := range Plan(cfg, restored, now) {
			switch a := a.(type) {
			case BuildTestRef:
				if !reflect.DeepEqual(a.PRs, prs[1:2]) {
					t.Fatal("wrong resumed batch")
				}
			case BuildChainedTestRef:
				if !reflect.DeepEqual(a.PRs, prs[1:2]) {
					t.Fatal("wrong resumed chain")
				}
			}
		}
	}
}
