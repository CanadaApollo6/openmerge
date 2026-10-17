package executor_test

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestReviewLedgerRecordsRerunsReturnedTogether(t *testing.T) {
	c := newChainExecutor(t, 1)
	b := c.state.Active
	j := store.Journal{Seed: c.state}
	c.f.SetCheckRuns(repo, b.TestedSHA, []forge.CheckRun{
		{ID: 101, Attempt: 1, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: b.TestRef},
		{ID: 102, Attempt: 2, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: b.TestRef},
	})
	c.x.StartedTestingAt = b.StartedTestingAt
	must(t, c.x.Execute(t.Context(), plannedChainAction[queue.RefreshChecks](t, c, b.ID), func(e queue.Event) error {
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Event: e})
		return nil
	}))
	q, _, _ := status.Project(j, 0, 0, nil)
	entry := q.LedgerEntry(b.PRs[0].PR)
	if entry == nil || len(entry.RunLedger) != 2 {
		if entry != nil {
			for _, row := range entry.RunLedger {
				t.Logf("ledger run: id=%d attempt=%d outcome=%s", row.Identity.ID, row.Identity.Attempt, row.Outcome)
			}
		}
		t.Fatal("forge returned attempts 101/1 and 102/2 together; ledger must record both")
	}
}

func TestReviewRetiredCommitStatusFollowupCanSettle(t *testing.T) {
	h := setup(t)
	a := queue.ObserveRetiredChecks{BatchID: "b1", TestedSHA: "sha", TestRef: "openmerge/queue/main/b1", Reason: "closed", Runs: []queue.CheckRun{{ID: 101, Attempt: 1, Name: "ci", Source: "status:ci", State: "pending"}}}
	h.f.SetCheckRuns(repo, a.TestedSHA, []forge.CheckRun{{ID: 101, Attempt: 1, Name: "ci", Source: "status:ci", State: forge.Success}})
	var events []queue.Event
	if err := h.x.Execute(t.Context(), a, record(&events)); err != nil {
		t.Fatalf("terminal status returned without a branch (the commit-status contract), but follow-up stays pending: %v; events=%d", err, len(events))
	}
}
