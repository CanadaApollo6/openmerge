package executor_test

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
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
