package store

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCancellationIntentSurvivesWorkerRestart(t *testing.T) {
	url := databaseURL(t)
	writer := openTestStore(t, url)
	q := ensureQueue(t, writer, "")
	cfg := config
	cfg.SpeculationMaxDepth = 4
	appendEvents(t, writer, q, seedEvents()...)
	a := queue.BuildChainedTestRef{}
	plans, err := writer.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	for _, plan := range plans {
		if build, ok := plan.Action.(queue.BuildChainedTestRef); ok {
			a = build
		}
	}
	if a.BatchID == "" {
		t.Fatal("missing admission")
	}
	appendEvents(t, writer, q, chainStart(a), queue.ChainedTestRefBuilt{BatchID: a.BatchID, TestedSHA: "tested", LandingSHA: "tested", ParentBatchID: a.ParentBatchID, Position: a.Position, Method: queue.Squash, At: epoch}, queue.ChecksObserved{BatchID: a.BatchID, TestedSHA: "tested", Runs: []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}}}, queue.PRClosed{PR: 1})
	state, _, err := writer.Load(t.Context(), q)
	must(t, err)
	for _, action := range queue.Plan(cfg, state, epoch) {
		if invalidation, ok := action.(queue.InvalidateBatches); ok {
			appendEvents(t, writer, q, queue.BatchesInvalidated(invalidation))
		}
	}
	state, _, err = writer.Load(t.Context(), q)
	must(t, err)
	var intent queue.CheckCancellationRequested
	for _, action := range queue.Plan(cfg, state, epoch) {
		if request, ok := action.(queue.RecordCheckCancellation); ok {
			intent = queue.CheckCancellationRequested(request)
		}
	}
	if intent.BatchID == "" {
		t.Fatal("missing cancellation intent")
	}
	appendEvents(t, writer, q, intent)
	// A new store instance has no shared projection or worker memory.
	reader := openTestStore(t, url)
	plans, err = reader.Plan(t.Context(), q, cfg, epoch.Add(time.Minute))
	must(t, err)
	found := false
	for _, plan := range plans {
		if cancel, ok := plan.Action.(queue.CancelChecks); ok {
			found = true
			if cancel.TestedSHA != "tested" || cancel.Runs[0].ID != 7 {
				t.Fatal("restart changed cancellation targets")
			}
		}
	}
	if !found {
		t.Fatal("restart lost durable cancellation")
	}
	confirmed := intent.Runs
	confirmed[0].State = "cancelled"
	appendEvents(t, reader, q, queue.CheckCancellationReported{BatchID: intent.BatchID, TestedSHA: intent.TestedSHA, Confirmed: confirmed})
	state, _, err = writer.Load(t.Context(), q)
	must(t, err)
	for _, a := range queue.Plan(cfg, state, epoch) {
		if _, ok := a.(queue.CancelChecks); ok {
			t.Fatal("completion not durable")
		}
	}
}
