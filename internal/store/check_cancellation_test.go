package store

import (
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
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

type cancellationReportDriver struct {
	forge.Driver
	unsupported string
}

func (d cancellationReportDriver) CancelChecks(context.Context, forge.RepoRef, forge.CancelChecksRequest) (forge.CancelChecksResult, error) {
	return forge.CancelChecksResult{Unsupported: d.unsupported}, nil
}
func TestCancellationEmptyReportsPersistAndSurviveRestart(t *testing.T) {
	for _, mode := range []string{"pending", "unsupported", "permission-fallback", "missing"} {
		t.Run(mode, func(t *testing.T) {
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
			var driver forge.Driver = cancellationReportDriver{unsupported: map[string]string{"unsupported": "unsupported producer", "permission-fallback": "Actions permission not granted"}[mode]}
			if mode == "missing" {
				driver = struct{ forge.Driver }{}
			}
			_, err = writer.Plan(t.Context(), q, cfg, epoch)
			must(t, err)
			claims, err := writer.ClaimQueue(t.Context(), q, 100, time.Minute, epoch)
			must(t, err)
			var claim ClaimedAction
			for _, candidate := range claims {
				if candidate.Key == queue.CancelChecks(intent).Key() {
					claim = candidate
				}
			}
			if claim.Key == "" {
				t.Fatal("missing persistent cancellation claim")
			}
			emit := writer.Emit(t.Context(), claim, cfg)
			x := executor.Executor{Driver: driver, AllowNoRequiredChecks: true}
			err = x.Execute(t.Context(), claim.Action, emit)
			if mode == "pending" {
				if !errors.Is(err, executor.ErrCancellationPending) {
					t.Fatal(err)
				}
				deferred, ok := queue.DeferCheckCancellation(claim.Action, epoch, err.Error())
				if !ok {
					t.Fatal("missing durable retry")
				}
				must(t, emit(deferred))
			} else {
				must(t, err)
			}
			must(t, writer.Complete(t.Context(), claim, Done))
			reader := openTestStore(t, url)
			state, _, err = reader.Load(t.Context(), q)
			must(t, err)
			if state.Batch(intent.BatchID) != nil {
				t.Fatal("retired batch revived")
			}
			if mode == "pending" {
				appendEvents(t, reader, q, queue.PRObserved{PR: 2, HeadSHA: "new-head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: epoch})
				plans, err := reader.Plan(t.Context(), q, cfg, epoch.Add(29*time.Second))
				must(t, err)
				for _, plan := range plans {
					if _, ok := plan.Action.(queue.RetryCheckCancellation); ok {
						t.Fatal("retry persisted before wake")
					}
				}
				next, err := reader.ClaimQueue(t.Context(), q, 100, time.Minute, epoch.Add(29*time.Second))
				must(t, err)
				if len(next) == 0 {
					t.Fatal("backoff prevented new work from being claimed")
				}
				for _, c := range next {
					if c.Key == claim.Key {
						t.Fatal("failed cancellation stayed first in claim order")
					}
				}
				plans, err = reader.Plan(t.Context(), q, cfg, epoch.Add(30*time.Second))
				must(t, err)
				found := false
				for _, plan := range plans {
					if retry, ok := plan.Action.(queue.RetryCheckCancellation); ok {
						found = true
						if retry.Attempt != 1 {
							t.Fatal("restart lost retry count")
						}
					}
				}
				if !found {
					t.Fatal("restart lost scheduled cancellation")
				}
			} else {
				_, err = reader.Plan(t.Context(), q, cfg, epoch)
				must(t, err)
			}
		})
	}
}
