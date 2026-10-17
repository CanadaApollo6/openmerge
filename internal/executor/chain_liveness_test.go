package executor_test

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type chainExecutor struct {
	*harness
	cfg   queue.Config
	state queue.State
	now   time.Time
}

func newChainExecutor(t *testing.T, count int) *chainExecutor {
	t.Helper()
	h := setup(t)
	c := &chainExecutor{harness: h, cfg: queue.Config{MergeMethod: queue.Merge, SpeculationMaxDepth: 4, CheckTimeout: time.Minute}, now: epoch}
	c.state = queue.State{BaseBranch: "main", BaseSHA: h.base, Fence: h.fence}
	c.x.RequiredChecks = []string{"ci"}
	c.x.ChecksRetryFailed, c.x.CheckTimeout = 1, c.cfg.CheckTimeout
	c.x.Now = func() time.Time { return c.now }
	for _, pr := range h.prs[:count] {
		c.state = queue.Apply(c.state, queue.PRObserved{PR: pr.Number, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
		c.state = queue.Apply(c.state, queue.EnqueueRequested{PR: pr.Number, At: c.now})
	}
	for range count {
		c.execute(t, plannedChainAction[queue.BuildChainedTestRef](t, c, ""))
	}
	return c
}

func plannedChainAction[T queue.Action](t *testing.T, c *chainExecutor, batchID string) T {
	t.Helper()
	for _, action := range queue.Plan(c.cfg, c.state, c.now) {
		if selected, ok := action.(T); ok {
			b := c.state.ActionBatch(action)
			if failed, ok := action.(queue.FailBatch); ok {
				b = c.state.Batch(failed.BatchID)
			}
			if batchID == "" || b != nil && b.ID == batchID {
				return selected
			}
		}
	}
	var zero T
	t.Fatalf("missing %T for %s at %s: %#v", zero, batchID, c.now, queue.Plan(c.cfg, c.state, c.now))
	return zero
}

func (c *chainExecutor) execute(t *testing.T, action queue.Action) {
	t.Helper()
	if b := c.state.ActionBatch(action); b != nil {
		c.x.StartedTestingAt, c.x.CheckRetry = b.StartedTestingAt, b.CheckRetry
	}
	must(t, c.x.Execute(t.Context(), action, func(e queue.Event) error {
		switch e.(type) {
		case queue.ChainedBatchStarted, queue.CheckRerunRequested, queue.CheckRerunDispatchProgress, queue.LandingStarted:
			if !queue.CanStart(c.cfg, c.state, action, e) {
				t.Fatalf("unauthorized %T", e)
			}
		}
		c.state = queue.Apply(c.state, e)
		return nil
	}))
}

func (c *chainExecutor) checks(b *queue.Batch, id, attempt uint64, state forge.CheckState) {
	c.f.SetCheckRuns(repo, b.TestedSHA, []forge.CheckRun{{ID: id, Attempt: attempt, Name: "ci", Source: "app:ci", State: state, HeadBranch: b.TestRef}})
}

func TestProvisionalTimeoutStaleExecutorReadConfirmsAtDeadline(t *testing.T) {
	c := newChainExecutor(t, 2)
	first, second := c.state.Batches()[0], c.state.Batches()[1]
	// Retain a real planned refresh to model a read already in flight when
	// the predecessor settles and the timeout becomes actionable at the head.
	staleRead := plannedChainAction[queue.RefreshChecks](t, c, second.ID)
	c.checks(first, 1, 1, forge.Failure)
	c.now = epoch.Add(59 * time.Second)
	c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, first.ID))
	c.execute(t, plannedChainAction[queue.RecordCheckRerun](t, c, first.ID))
	c.execute(t, plannedChainAction[queue.RerunChecks](t, c, first.ID))
	c.checks(second, 2, 2, forge.Pending)
	c.now = epoch.Add(61 * time.Second)
	c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, second.ID))
	c.execute(t, plannedChainAction[queue.FailBatch](t, c, second.ID))
	if c.state.Batch(second.ID).ProvisionalFailure != queue.ChecksTimeout {
		t.Fatal("later batch did not provisionally time out")
	}
	c.checks(first, 2, 2, forge.Success)
	c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, first.ID))
	c.execute(t, plannedChainAction[queue.Land](t, c, first.ID))
	c.checks(second, 1, 1, forge.Failure)
	c.execute(t, staleRead)
	b := c.state.Batch(second.ID)
	if !b.CheckRetry.OriginalDeadline.Equal(epoch.Add(time.Minute)) || b.CheckRetry.ObservedRuns[0].Attempt != 2 || b.CheckRetry.Requested {
		t.Fatalf("stale read replaced evidence or extended the timer: %+v", b.CheckRetry)
	}
	confirm := plannedChainAction[queue.ConfirmCheckFailure](t, c, second.ID)
	if confirm.Reason != queue.ChecksTimeout || !c.now.Equal(epoch.Add(61*time.Second)) {
		t.Fatal("timeout was not confirmed immediately on promotion")
	}
	c.execute(t, confirm)
	if c.state.PendingDequeue(second.PRs[0].PR) != queue.ChecksTimeout {
		t.Fatal("timeout confirmation did not record the diagnosis")
	}
}

func TestDuplicateLegacyFailureAfterRequestDispatchesAndTimesOut(t *testing.T) {
	c := newChainExecutor(t, 1)
	b := c.state.Active
	c.checks(b, 1, 1, forge.Failure)
	c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, b.ID))
	c.execute(t, plannedChainAction[queue.RecordCheckRerun](t, c, b.ID))
	requested := *c.state.Active.CheckRetry
	legacy := queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: false}
	c.state = queue.Apply(c.state, legacy)
	// A delayed aggregate failure must not strand the durable rerun intent.
	c.execute(t, plannedChainAction[queue.RerunChecks](t, c, b.ID))
	if !c.state.Active.CheckRetry.Dispatched || !c.now.Before(requested.Deadline) {
		t.Fatal("rerun was not dispatched within its deadline")
	}
	c.state = queue.Apply(c.state, legacy)
	c.checks(b, 1, 1, forge.Failure)
	c.now = requested.Deadline.Add(time.Nanosecond)
	c.execute(t, plannedChainAction[queue.RefreshChecks](t, c, b.ID))
	if r := c.state.Active.CheckRetry; !r.OriginalDeadline.Equal(requested.OriginalDeadline) || !r.Deadline.Equal(requested.Deadline) || !r.RequestedAt.Equal(requested.RequestedAt) {
		t.Fatal("duplicate failure or stale read moved the recorded deadlines")
	}
	confirm := plannedChainAction[queue.ConfirmCheckFailure](t, c, b.ID)
	if confirm.Reason != queue.ChecksTimeout {
		t.Fatal("stale original failure authorized fresh blame")
	}
	c.execute(t, confirm)
	if c.state.PendingDequeue(b.PRs[0].PR) != queue.ChecksTimeout {
		t.Fatal("requested rerun did not terminate at its deadline")
	}
}
