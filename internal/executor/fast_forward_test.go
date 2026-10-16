package executor_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFastForwardQueueCompositionAndLanding(t *testing.T) {
	for _, scenario := range []string{"stack", "siblings", "linear-prefix", "diverged", "base-race", "lost-result", "build-error", "selection-emit-error"} {
		t.Run(scenario, func(t *testing.T) {
			h := setup(t)
			pins := []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}
			if scenario == "stack" || scenario == "linear-prefix" {
				pr, err := h.f.OpenPR(repo, 4, pins[0].HeadSHA, true)
				must(t, err)
				must(t, h.f.SetPRMetadata(repo, 4, "main", false, "contributor"))
				pins = append(pins, queue.BatchPR{PR: 4, HeadSHA: pr.HeadSHA})
			}
			if scenario == "siblings" || scenario == "linear-prefix" {
				pins = append(pins, queue.BatchPR{PR: 2, HeadSHA: h.prs[1].HeadSHA})
			}
			state := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			cfg := queue.Config{MergeMethod: queue.FastForward, BatchMaxSize: len(pins)}
			for _, pin := range pins {
				state = queue.Apply(state, queue.PRObserved{PR: pin.PR, HeadSHA: pin.HeadSHA, Eligible: true, Trusted: true})
				state = queue.Apply(state, queue.EnqueueRequested{PR: pin.PR, At: epoch})
			}
			if scenario == "diverged" {
				must(t, h.f.SetRef(repo, "main", h.prs[2].HeadSHA))
				state = queue.Apply(state, queue.BaseMoved{SHA: h.prs[2].HeadSHA})
			}
			emit := func(event queue.Event) error {
				if _, selected := event.(queue.FastForwardBatchSelected); selected && scenario == "selection-emit-error" {
					return errLog
				}
				state = queue.Apply(state, event)
				return nil
			}
			if scenario == "build-error" {
				h.f.FailNext("BuildTestRef", errIO)
			}
			build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
			err := h.x.Execute(t.Context(), build, emit)
			if scenario == "build-error" || scenario == "selection-emit-error" {
				if err == nil {
					t.Fatal("build/emit failure swallowed")
				}
				if _, err := h.f.ReadRef(t.Context(), repo, build.Ref); err == nil {
					t.Fatal("failed build pushed")
				}
				return
			}
			must(t, err)
			if scenario == "diverged" {
				if state.PendingDequeue(1) != queue.FastForwardRequired || state.Active != nil {
					t.Fatal("diverged head not dequeued")
				}
				return
			}
			selected := pins
			if scenario == "siblings" {
				selected = pins[:1]
			}
			if scenario == "linear-prefix" {
				selected = pins[:2]
			}
			head := selected[len(selected)-1].HeadSHA
			if state.Active == nil || !slices.Equal(state.Active.PRs, selected) || state.Active.TestedSHA != head {
				t.Fatalf("wrong composition: %+v", state.Active)
			}
			h.f.SetCheckRuns(repo, head, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			state = queue.Apply(state, queue.ChecksCompleted{TestedSHA: head, Success: true})
			land := queue.Plan(cfg, state, epoch)[0].(queue.Land)
			if scenario == "base-race" {
				must(t, h.f.SetRef(repo, "main", h.prs[2].HeadSHA))
			}
			dropped := false
			must(t, h.x.Execute(t.Context(), land, func(event queue.Event) error {
				if _, ok := event.(queue.Landed); ok && scenario == "lost-result" {
					dropped = true
					return nil
				}
				return emit(event)
			}))
			if scenario == "base-race" {
				if h.read(t, "main") != h.prs[2].HeadSHA || state.Active != nil {
					t.Fatal("base race landed or not fenced")
				}
				state = queue.Apply(state, queue.BaseMoved{SHA: h.prs[2].HeadSHA})
				for _, a := range queue.Plan(cfg, state, epoch) {
					if b, ok := a.(queue.BuildTestRef); ok {
						must(t, h.x.Execute(t.Context(), b, emit))
					}
				}
				if state.PendingDequeue(1) != queue.FastForwardRequired {
					t.Fatal("base-race retry rebased head")
				}
				return
			}
			if dropped {
				must(t, h.x.Execute(t.Context(), queue.ResolveLanding{BatchID: land.BatchID, AttemptID: "attempt", TestedSHA: head, FenceToken: land.FenceToken}, emit))
			}
			if h.read(t, "main") != head || state.Active != nil || h.read(t, fenceRef) == h.fence {
				t.Fatal("did not land unchanged head through fence")
			}
			for _, a := range queue.Plan(cfg, state, epoch) {
				if _, ok := a.(queue.MarkLanded); ok {
					t.Fatal("fast-forward scheduled API closure")
				}
				if cleanup, ok := a.(queue.DeleteHeadBranch); ok {
					must(t, h.x.Execute(t.Context(), cleanup, emit))
					pr, err := h.f.PullRequest(t.Context(), repo, cleanup.PR)
					must(t, err)
					if _, err := h.f.ReadRef(t.Context(), repo, pr.HeadBranch); err == nil {
						t.Fatal("landed head retained")
					}
				}
			}
		})
	}
}

func TestFastForwardAncestryReadFailureIsRetryable(t *testing.T) {
	h := setup(t)
	h.f.FailNext("IsAncestor", errIO)
	a := h.buildAction()
	a.Method = queue.FastForward
	a.PRs = append(a.PRs, queue.BatchPR{PR: 2, HeadSHA: h.prs[1].HeadSHA})
	var events []queue.Event
	err := h.x.Execute(t.Context(), a, record(&events))
	if !errors.Is(err, errIO) {
		t.Fatal(err)
	}
	if failed, ok := events[len(events)-1].(queue.TestRefBuildFailed); !ok || failed.Conflict {
		t.Fatalf("wrong retry result: %+v", events)
	}
}
