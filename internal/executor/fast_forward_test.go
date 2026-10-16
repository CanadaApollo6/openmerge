package executor_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFastForwardRequiresMarkerCI(t *testing.T) {
	for _, scenario := range []string{"head same App", "head other App", "historical same ref and SHA", "completed rerun", "fork null head_branch", "commit statuses"} {
		t.Run(scenario, func(t *testing.T) {
			h := setup(t)
			state := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			cfg := queue.Config{MergeMethod: queue.FastForward}
			head := h.prs[0].HeadSHA
			state = queue.Apply(state, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
			state = queue.Apply(state, queue.EnqueueRequested{PR: 1, At: epoch})
			build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
			source := "app:11"
			if scenario == "head other App" {
				source = "app:22"
			}
			if scenario == "commit statuses" {
				source = "status:ci"
			}
			if scenario == "fork null head_branch" {
				must(t, h.f.SetPRMetadata(repo, 1, "main", false, "contributor"))
			}
			h.f.SetCheckRuns(repo, head, []forge.CheckRun{{ID: 100, Name: "ci", Source: source, State: forge.Success}, {ID: 101, Name: "lint", Source: source, State: forge.Success}})
			emit := func(e queue.Event) error { state = queue.Apply(state, e); return nil }
			must(t, h.x.Execute(t.Context(), build, emit))
			marker := state.Active.TestedSHA
			if marker == head || h.read(t, build.Ref) != marker {
				t.Fatal("test ref must publish a new marker")
			}
			must(t, h.x.Execute(t.Context(), queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: marker}, emit))
			for _, a := range queue.Plan(cfg, state, epoch) {
				if _, ok := a.(queue.Land); ok {
					t.Fatal("head CI authorized landing without marker CI")
				}
			}
			land := queue.Land{BatchID: build.BatchID, TestedSHA: marker, ExpectedBaseSHA: build.BaseSHA, Ref: build.Ref, Method: queue.FastForward, PRs: build.PRs, FenceToken: h.fence}
			if err := h.x.Execute(t.Context(), land, emit); !errors.Is(err, executor.ErrChecksNotSuccessful) {
				t.Fatalf("final guard: %v", err)
			}
			if h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence {
				t.Fatal("head CI moved landing refs")
			}
			// Both statuses and runs count normally on this attempt's unique marker.
			h.f.SetCheckRuns(repo, marker, []forge.CheckRun{{ID: 1, Name: "ci", Source: source, State: forge.Success, HeadBranch: build.Ref}, {ID: 2, Name: "lint", Source: source, State: forge.Success, HeadBranch: build.Ref}})
			must(t, h.x.Execute(t.Context(), queue.RefreshChecks{BatchID: build.BatchID, TestedSHA: marker}, emit))
			must(t, h.x.Execute(t.Context(), land, emit))
			if h.read(t, "main") != head {
				t.Fatal("marker CI did not land the pinned head")
			}
		})
	}
}

func TestFastForwardLinearHistoryBuildValidation(t *testing.T) {
	for _, scenario := range []string{"required", "required linear", "second head", "not required", "unknown", "graph read fails", "emit fails"} {
		t.Run(scenario, func(t *testing.T) {
			h := setup(t)
			merged, err := h.f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "test-merge", BaseSHA: h.base, PRs: h.prs[:1], Method: forge.Merge, BatchID: "merge"})
			must(t, err)
			pr, err := h.f.OpenPR(repo, 4, merged, true)
			must(t, err)
			pins := []queue.BatchPR{{PR: 4, HeadSHA: pr.HeadSHA}}
			if scenario == "required linear" {
				pins = []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}
			}
			if scenario == "second head" {
				pins = append([]queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}, pins...)
			}
			state := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			for _, pin := range pins {
				state = queue.Apply(state, queue.PRObserved{PR: pin.PR, HeadSHA: pin.HeadSHA, Eligible: true, Trusted: true})
				state = queue.Apply(state, queue.EnqueueRequested{PR: pin.PR, At: epoch})
			}
			cfg := queue.Config{MergeMethod: queue.FastForward, BatchMaxSize: len(pins)}
			build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
			h.f.SetLinearHistoryRequired(repo, "main", scenario != "not required")
			if scenario == "unknown" {
				h.f.FailNext("LinearHistoryRequired", errIO)
			}
			if scenario == "graph read fails" {
				h.f.FailNext("HasMergeCommits", errIO)
			}
			err = h.x.Execute(t.Context(), build, func(e queue.Event) error {
				if _, blocked := e.(queue.FastForwardMergeCommitsBlocked); blocked && scenario == "emit fails" {
					return errLog
				}
				state = queue.Apply(state, e)
				return nil
			})
			if scenario == "graph read fails" || scenario == "emit fails" {
				if err == nil {
					t.Fatal("failure swallowed")
				}
			} else {
				must(t, err)
			}
			if scenario == "not required" || scenario == "unknown" || scenario == "required linear" {
				if h.read(t, build.Ref) == pins[0].HeadSHA || state.Active.Phase != queue.Testing {
					t.Fatal("unknown/disabled requirement changed build behavior")
				}
				return
			}
			if _, err := h.f.ReadRef(t.Context(), repo, build.Ref); err == nil {
				t.Fatal("merge head published before build validation")
			}
			if scenario == "graph read fails" || scenario == "emit fails" {
				return
			}
			if state.Active != nil || state.PendingDequeue(4) != queue.MergeCommitsNotAllowed || state.PendingDequeue(1) != "" {
				t.Fatal("offending head not exclusively dequeued")
			}
			if scenario == "second head" {
				for _, action := range queue.Plan(cfg, state, epoch) {
					if retry, ok := action.(queue.BuildTestRef); ok {
						must(t, h.x.Execute(t.Context(), retry, func(e queue.Event) error { state = queue.Apply(state, e); return nil }))
					}
				}
				if state.Active == nil || state.Active.TestedSHA == pins[0].HeadSHA {
					t.Fatal("valid prefix lost after rejecting merge head")
				}
			}
		})
	}
}

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
			if state.Active == nil || !slices.Equal(state.Active.PRs, selected) || state.Active.TestedSHA == head {
				t.Fatalf("wrong composition: %+v", state.Active)
			}
			marker := state.Active.TestedSHA
			h.f.SetCheckRuns(repo, marker, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			state = queue.Apply(state, queue.ChecksCompleted{TestedSHA: marker, Success: true})
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
				// Replay the persisted recovery action with changed configuration.
				actions := queue.Plan(queue.Config{MergeMethod: queue.Squash}, state, epoch.Add(time.Hour))
				for _, a := range actions {
					if resolve, ok := a.(queue.FastForwardResolveLanding); ok {
						_, payload, err := queue.EncodeAction(resolve)
						must(t, err)
						restored, err := queue.DecodeAction(payload)
						must(t, err)
						must(t, h.x.Execute(t.Context(), restored, emit))
					}
				}
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
