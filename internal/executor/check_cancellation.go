package executor

import (
	"context"
	"errors"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ErrCancellationPending keeps the durable intent retryable until the provider
// reports completion. A cancel POST alone never supplies that evidence.
var ErrCancellationPending = errors.New("CI cancellation requested; completion is not yet confirmed")

func (x *Executor) cancelChecks(ctx context.Context, a queue.CancelChecks, emit func(queue.Event) error) error {
	report := queue.CheckCancellationReported{BatchID: a.BatchID, TestedSHA: a.TestedSHA}
	canceller, ok := x.Driver.(forge.CheckCanceller)
	if !ok {
		report.Unsupported = true
		report.Detail = "forge does not support CI cancellation"
		return emit(report)
	}
	runs := make([]forge.CheckRun, len(a.Runs))
	for i, r := range a.Runs {
		runs[i] = toForgeCheckRun(r)
		runs[i].HeadBranch = a.TestRef
	}
	result, err := canceller.CancelChecks(ctx, x.Repo, forge.CancelChecksRequest{TestedSHA: a.TestedSHA, TestRef: a.TestRef, Runs: runs, Key: a.Key()})
	if err != nil {
		return err
	}
	report.Unsupported, report.Detail = result.Unsupported != "", result.Unsupported
	for _, confirmed := range result.Confirmed {
		// A driver's answer cannot broaden the durable target set.
		index := slices.IndexFunc(a.Runs, func(r queue.CheckRun) bool {
			return r.ID == confirmed.ID && canonicalCheckAttempt(r.Attempt) == canonicalCheckAttempt(confirmed.Attempt) && r.Source == confirmed.Source && r.Name == confirmed.Name && r.WorkflowRunID == confirmed.WorkflowRunID
		})
		if index < 0 || confirmed.State == forge.Pending {
			return errors.New("forge returned an unrequested or pending cancellation confirmation")
		}
		run := a.Runs[index]
		run.State = string(confirmed.State)
		if !slices.ContainsFunc(report.Confirmed, func(r queue.CheckRun) bool {
			return r.ID == run.ID && r.Source == run.Source && r.Name == run.Name && r.Attempt == run.Attempt
		}) {
			report.Confirmed = append(report.Confirmed, run)
		}
	}
	if err := emit(report); err != nil {
		return err
	}
	if !report.Unsupported && len(report.Confirmed) != len(a.Runs) {
		return ErrCancellationPending
	}
	return nil
}

func (x *Executor) observeRetiredChecks(ctx context.Context, a queue.ObserveRetiredChecks, emit func(queue.Event) error) error {
	var checks []forge.CheckRun
	var err error
	if reader, ok := x.Driver.(forge.RetryCheckReader); ok {
		var workflows []uint64
		for _, run := range a.Runs {
			if run.WorkflowRunID != 0 && !slices.Contains(workflows, run.WorkflowRunID) {
				workflows = append(workflows, run.WorkflowRunID)
			}
		}
		checks, err = reader.CheckRunsForRetry(ctx, x.Repo, a.TestedSHA, workflows)
	} else {
		checks, err = x.Driver.CheckRuns(ctx, x.Repo, a.TestedSHA)
	}
	if err != nil {
		return err
	}
	observed := make([]queue.CheckRun, 0, len(a.Runs))
	terminal, success := true, true
	for _, target := range a.Runs {
		index := slices.IndexFunc(checks, func(r forge.CheckRun) bool {
			return r.ID == target.ID && canonicalCheckAttempt(r.Attempt) == canonicalCheckAttempt(target.Attempt) && r.Source == target.Source && r.Name == target.Name && r.WorkflowRunID == target.WorkflowRunID && r.HeadBranch == a.TestRef
		})
		if index < 0 {
			terminal = false
			continue
		}
		run := target
		run.State = string(checks[index].State)
		if run.State == "pending" {
			terminal = false
		}
		if run.State != "success" {
			success = false
		}
		observed = append(observed, run)
	}
	// Retired IDs are never admitted again. These observations deliberately have
	// no timers: the reducer ignores them and status retains historical outcomes.
	if len(observed) > 0 {
		if err := emit(queue.ChecksObserved{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Runs: observed, Terminal: terminal, Success: terminal && success}); err != nil {
			return err
		}
	}
	if !terminal {
		return ErrCancellationPending
	}
	return emit(queue.ActionCompleted{Key: a.Key()})
}
