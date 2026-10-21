package executor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLandingChecksShareRetryLedgerRead(t *testing.T) {
	for _, outcome := range []string{"pass", "stale-original", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			c := newChainExecutor(t, 1)
			b := c.state.Active
			c.checks(b, 1, 1, forge.Failure)
			c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, b.ID))
			c.execute(t, plannedChainAction[queue.RecordCheckRerun](t, c, b.ID))
			c.execute(t, plannedChainAction[queue.RerunChecks](t, c, b.ID))
			c.checks(b, 1, 2, forge.Success)
			c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, b.ID))
			land := plannedChainAction[queue.Land](t, c, b.ID)
			state := c.state
			stopped := errors.New("captured exact gate inputs")
			calls := 0
			c.x.CheckRetry = state.Active.CheckRetry
			c.x.LandingDecisions = &executor.LandingDecisionGate{Policy: cp.Policy{Approvals: &cp.Approvals{}}, Load: func(context.Context) (queue.State, error) { return state, nil }, ReadTestedMessages: func(context.Context, queue.Land) ([]string, error) { return nil, stopped }, Commit: func(context.Context, []queue.ApprovalEligibilityObserved, []queue.LandingDecisionRecorded, queue.LandingStarted) error {
				return stopped
			}, ReadInputsWithChecks: func(_ context.Context, _ queue.Land, _ queue.State, runs []forge.CheckRun, at time.Time) ([]queue.ApprovalEligibilityObserved, error) {
				calls++
				if len(runs) != 1 || runs[0].ID != 1 || runs[0].Attempt != 2 || runs[0].State != forge.Success || !at.Equal(c.now) {
					t.Fatal("gate diverged from retry identity ledger", runs, at)
				}
				return nil, stopped
			}}
			switch outcome {
			case "stale-original":
				c.checks(b, 1, 1, forge.Success)
			case "cancelled":
				c.checks(b, 1, 2, forge.Cancelled)
			}
			// A newer successful run on a contributor branch must never enter the
			// evidence or supersede the retry read used by the queue policy.
			runs, err := c.f.CheckRuns(t.Context(), repo, b.TestedSHA)
			must(t, err)
			runs = append(runs, forge.CheckRun{ID: 999, Attempt: 99, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: "contributor"})
			c.f.SetCheckRuns(repo, b.TestedSHA, runs)
			err = c.x.Execute(t.Context(), land, func(e queue.Event) error { c.state = queue.Apply(c.state, e); return nil })
			if outcome == "pass" {
				if !errors.Is(err, stopped) || calls != 1 {
					t.Fatal("passing rerun did not reach exact gate", err, calls)
				}
			} else if !errors.Is(err, executor.ErrChecksNotSuccessful) || calls != 0 {
				t.Fatal("stale/cancelled rerun entered evidence gate", err, calls)
			}
		})
	}
}
