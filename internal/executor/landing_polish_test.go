package executor_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestHeadCleanupRevalidatesLandingAndRetries(t *testing.T) {
	for _, scenario := range []string{"delete", "disabled", "rollback", "read-error", "delete-error", "emit-error", "protected", "targeted", "open"} {
		t.Run(scenario, func(t *testing.T) {
			h := setup(t)
			tested := h.tested(t)
			must(t, h.f.SetRef(repo, "main", tested))
			must(t, h.f.ClosePR(repo, 1))
			a := queue.DeleteHeadBranch{PR: 1, HeadSHA: h.prs[0].HeadSHA, LandedSHA: tested}
			switch scenario {
			case "disabled":
				h.x.DisableDeleteHeadBranch = true
				h.f.FailNext("HeadBranchForDeletion", errIO)
			case "rollback":
				must(t, h.f.SetRef(repo, "main", h.base))
			case "read-error":
				h.f.FailNext("HeadBranchForDeletion", errIO)
			case "delete-error":
				h.f.FailNext("DeleteBranch", errIO)
			case "protected":
				h.f.SetBranchProtected(repo, "pr-1", true)
			case "targeted":
				must(t, h.f.SetPRMetadata(repo, 2, "pr-1", false, "contributor"))
			case "open":
				// A different, still-open PR is never cleaned by a landed action.
				a.PR, a.HeadSHA = 2, h.prs[1].HeadSHA
			}
			var events []queue.Event
			emit := record(&events)
			if scenario == "emit-error" {
				emit = func(queue.Event) error { return errLog }
			}
			err := h.x.Execute(t.Context(), a, emit)
			failed := scenario == "rollback" || scenario == "read-error" || scenario == "delete-error" || scenario == "emit-error" || scenario == "open"
			if (err != nil) != failed {
				t.Fatalf("cleanup err=%v events=%+v", err, events)
			}
			if scenario == "rollback" {
				var absent *executor.ErrLandingNotOnBase
				if !errors.As(err, &absent) {
					t.Fatalf("lost verification: %v", err)
				}
			}
			if failed && len(events) != 0 {
				t.Fatal("failed cleanup acknowledged")
			}
			if !failed && len(events) != 1 {
				t.Fatal("successful cleanup unacknowledged")
			}
			deleted := scenario == "delete" || scenario == "emit-error"
			if _, err := h.f.ReadRef(t.Context(), repo, "pr-1"); (err != nil) != deleted {
				t.Fatalf("branch deletion=%t, want %t", err != nil, deleted)
			}
			if deleted || scenario == "delete-error" || scenario == "read-error" {
				must(t, h.x.Execute(t.Context(), a, record(&events)))
				if _, err := h.f.ReadRef(t.Context(), repo, "pr-1"); err == nil {
					t.Fatal("retry failed to clean up")
				}
			}
		})
	}
}

func TestHeadCleanupIntentRecoversAfterEmitFailure(t *testing.T) {
	for _, failIntent := range []bool{true, false} {
		t.Run(map[bool]string{true: "intent", false: "landed"}[failIntent], func(t *testing.T) {
			h := setup(t)
			head := h.prs[0].HeadSHA
			s := queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
			s = queue.Apply(s, queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true})
			s = queue.Apply(s, queue.EnqueueRequested{PR: 1, At: epoch})
			emit := func(e queue.Event) error {
				_, data, err := queue.EncodeEvent(e)
				if err != nil {
					return err
				}
				e, err = queue.DecodeEvent(data)
				if err == nil {
					s = queue.Apply(s, e)
				}
				return err
			}
			build := h.buildAction()
			build.BatchID, build.Ref = "b1", "openmerge/queue/main/b1"
			must(t, h.x.Execute(t.Context(), build, emit))
			tested := s.Active.TestedSHA
			h.f.SetCheckRuns(repo, tested, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			s = queue.Apply(s, queue.ChecksCompleted{TestedSHA: tested, Success: true})
			cfg := queue.Config{MergeMethod: queue.Merge}
			var land queue.Land
			for _, action := range queue.Plan(cfg, s, epoch) {
				if a, ok := action.(queue.Land); ok {
					land = a
				}
			}
			err := h.x.Execute(t.Context(), land, func(e queue.Event) error {
				_, intent := e.(queue.HeadCleanupRequested)
				_, landed := e.(queue.Landed)
				if failIntent && intent || !failIntent && landed {
					return errLog
				}
				return emit(e)
			})
			if !errors.Is(err, errLog) || s.Active == nil || s.Active.Phase != queue.Landing {
				t.Fatalf("lost landing result did not remain recoverable: %v, %+v", err, s)
			}
			cleanup := queue.DeleteHeadBranch{PR: 1, HeadSHA: head, LandedSHA: tested}
			hasCleanup := func() bool {
				return slices.ContainsFunc(queue.Plan(cfg, s, epoch), func(a queue.Action) bool { return a.Key() == cleanup.Key() })
			}
			if hasCleanup() {
				t.Fatal("cleanup scheduled before durable Landed")
			}
			must(t, h.x.Execute(t.Context(), queue.ResolveLanding{BatchID: "b1", AttemptID: "attempt", TestedSHA: tested, FenceToken: h.fence}, emit))
			if !hasCleanup() {
				t.Fatal("resolution lost cleanup intent")
			}
			must(t, h.x.Execute(t.Context(), cleanup, emit))
			if hasCleanup() {
				t.Fatal("acknowledged cleanup repeated")
			}
		})
	}
}
