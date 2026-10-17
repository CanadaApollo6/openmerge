package executor_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFastForwardChainUsesLandingHeadsAndSettledFence(t *testing.T) {
	h := setup(t)
	first := h.prs[0]
	second, err := h.f.OpenPR(repo, 4, first.HeadSHA, true)
	must(t, err)
	cfg := queue.Config{MergeMethod: queue.FastForward, SpeculationMaxDepth: 4}
	state := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
	for _, pr := range []queue.BatchPR{{PR: 1, HeadSHA: first.HeadSHA}, {PR: 4, HeadSHA: second.HeadSHA}} {
		state = queue.Apply(state, queue.PRObserved{PR: pr.PR, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
		state = queue.Apply(state, queue.EnqueueRequested{PR: pr.PR, At: epoch})
	}
	emit := func(e queue.Event) error { state = queue.Apply(state, e); return nil }
	for range 2 {
		var action queue.BuildChainedTestRef
		for _, a := range queue.Plan(cfg, state, epoch) {
			if b, ok := a.(queue.BuildChainedTestRef); ok {
				action = b
			}
		}
		if action.BatchID == "" {
			t.Fatal("chain admission missing")
		}
		must(t, h.x.Execute(t.Context(), action, emit))
	}
	batches := state.Batches()
	if len(batches) != 2 || batches[1].BaseSHA != first.HeadSHA || batches[1].BaseSHA == batches[0].TestedSHA {
		t.Fatal("fast-forward successor built on marker")
	}
	for _, b := range batches {
		if b.TestedSHA == b.LandingCommit() {
			t.Fatal("marker was selected for landing")
		}
		h.f.SetCheckRuns(repo, b.TestedSHA, []forge.CheckRun{{ID: 1, Attempt: 1, Name: "ci", State: forge.Success, HeadBranch: b.TestRef}, {ID: 2, Attempt: 1, Name: "lint", State: forge.Success, HeadBranch: b.TestRef}})
		state = queue.Apply(state, queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: true})
	}
	for _, b := range batches {
		var land queue.Land
		for _, a := range queue.Plan(cfg, state, epoch) {
			if l, ok := a.(queue.Land); ok {
				land = l
			}
		}
		if land.BatchID != b.ID || land.FenceToken != state.Fence {
			t.Fatal("landing order/fence changed")
		}
		must(t, h.x.Execute(t.Context(), land, emit))
		if h.read(t, "main") != b.LandingCommit() {
			t.Fatal("did not land verified head")
		}
	}
	if state.Active != nil || len(state.Entries) != 0 {
		t.Fatal("chain did not settle")
	}
}
