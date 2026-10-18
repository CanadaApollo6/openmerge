package executor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestApprovalWaitOutcomeShortCircuitsRefreshAndLand(t *testing.T) {
	for _, retry := range []int{0, 1} {
		name := "retry-zero"
		if retry == 1 {
			name = "retry-one"
		}
		t.Run(name, func(t *testing.T) {
			h := setup(t)
			h.x.ChecksRetryFailed = retry
			sha := h.tested(t)
			h.f.SetCheckRuns(repo, sha, []forge.CheckRun{
				{Name: "ci", State: forge.Success, HeadBranch: h.buildAction().Ref},
				{Name: "lint", State: forge.Success, HeadBranch: h.buildAction().Ref},
			})
			calls := 0
			h.x.CheckPolicyOutcome = func([]forge.CheckRun) (executor.CheckOutcome, error) {
				calls++
				return executor.CheckOutcome{ApprovalWait: true}, executor.ErrLandingApprovals
			}

			var events []queue.Event
			if err := h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, record(&events)); err != nil {
				t.Fatalf("RefreshChecks error = %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("RefreshChecks events = %#v, want none", events)
			}
			if calls != 1 {
				t.Fatalf("policy calls after RefreshChecks = %d, want 1", calls)
			}

			events = nil
			land := queue.Land{
				BatchID:         "batch",
				ExpectedBaseSHA: h.base,
				TestedSHA:       sha,
				FenceToken:      h.fence,
				Method:          queue.Merge,
				PRs:             []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}},
			}
			if err := h.x.Execute(context.Background(), land, record(&events)); !errors.Is(err, executor.ErrLandingApprovals) {
				t.Fatalf("Land error = %v, want approval wait", err)
			}
			if len(events) != 0 {
				t.Fatalf("Land events = %#v, want none", events)
			}
			if calls != 2 {
				t.Fatalf("policy calls after Land = %d, want 2", calls)
			}
		})
	}
}
