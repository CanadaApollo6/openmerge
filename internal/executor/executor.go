// Package executor turns queue actions into forge calls and durable result events.
// The caller owns ADR 0002's watermark validation, locking, and action claims;
// Execute must only receive actions still authorized by that durable state.
package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Executor uses explicit dependencies so the same code runs with real or fake git.
// Nil Now uses time.Now. Each batch's stable landing key identifies its attempt.
type Executor struct {
	Driver         forge.Driver
	Git            forge.Git
	Repo           forge.RepoRef
	Base           string
	FenceRef       string
	RequiredChecks []string
	Now            func() time.Time
}

// Execute stops at the first failed emit. emit must durably record its event
// before returning nil. In particular, LandingStarted precedes the git push;
// after it is recorded an uncertain result is recovered from the fence commit.
// Infrastructure build errors emit TestRefBuildFailed and also return the error.
func (x *Executor) Execute(ctx context.Context, action queue.Action, emit func(queue.Event) error) error {
	switch a := action.(type) {
	case queue.BuildTestRef:
		prs := make([]forge.BuildPR, len(a.PRs))
		for i, pr := range a.PRs {
			prs[i] = forge.BuildPR{Number: pr.PR, HeadSHA: pr.HeadSHA}
		}
		sha, err := x.Git.BuildTestRef(ctx, x.Repo, forge.BuildRequest{
			Ref: a.Ref, BaseSHA: a.BaseSHA, PRs: prs, Method: forge.MergeMethod(a.Method), BatchID: a.BatchID,
		})
		if err != nil {
			conflict := errors.Is(err, forge.ErrConflict)
			if emitErr := emit(queue.TestRefBuildFailed{BatchID: a.BatchID, Conflict: conflict, At: x.now()}); emitErr != nil {
				return emitErr
			}
			if conflict {
				return nil
			}
			return err
		}
		return emit(queue.TestRefBuilt{BatchID: a.BatchID, TestedSHA: sha, Method: a.Method, At: x.now()})
	case queue.RefreshChecks:
		checks, err := x.Driver.CheckRuns(ctx, x.Repo, a.TestedSHA)
		if err != nil {
			return err
		}
		at := x.now()
		if terminal, success := x.checkResult(checks); terminal {
			if err := emit(queue.ChecksCompleted{TestedSHA: a.TestedSHA, Success: success}); err != nil {
				return err
			}
		}
		return emit(queue.ChecksRefreshed{BatchID: a.BatchID, TestedSHA: a.TestedSHA, At: at})
	case queue.Land:
		attemptID := a.Key()
		if err := emit(queue.LandingStarted{BatchID: a.BatchID, AttemptID: attemptID, FenceToken: a.FenceToken, At: x.now()}); err != nil {
			return err
		}
		sha, id, err := x.Git.ReadFence(ctx, x.Repo, x.FenceRef)
		if err != nil {
			return err
		}
		if id == "land/"+attemptID {
			return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: sha})
		}
		next, err := x.Git.Land(ctx, x.Repo, forge.LandRequest{
			Base: x.Base, ExpectedBaseSHA: a.ExpectedBaseSHA, TestedSHA: a.TestedSHA,
			Fence: x.FenceRef, ExpectedFence: a.FenceToken, NextFenceID: "land/" + attemptID,
		})
		if err != nil {
			var rejected *forge.LandRejectedError
			if errors.As(err, &rejected) {
				return emit(queue.LandingRejected{BatchID: a.BatchID, ActualBaseSHA: rejected.ActualBaseSHA, ActualFence: rejected.ActualFence})
			}
			return err
		}
		return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: next})
	case queue.ResolveLanding:
		return x.resolve(ctx, a, emit)
	case queue.DeleteRef:
		if err := x.Driver.DeleteRef(ctx, x.Repo, a.Ref); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.ReportStatus:
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, forge.Status{Context: "openmerge", State: a.Status, Detail: a.Detail}); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.Dequeue:
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, forge.Status{Context: "openmerge", State: "dequeued", Detail: string(a.Reason)}); err != nil {
			return err
		}
		return emit(queue.Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})
	default:
		return fmt.Errorf("unsupported queue action %T", action)
	}
}

// ErrFenceTampered reports a fence move belonging to neither outcome of the
// attempt. It indicates that ADR 0004's fence namespace protection is missing.
type ErrFenceTampered struct {
	AttemptID, ExpectedFence, ActualFence, ActualFenceID string
}

func (e *ErrFenceTampered) Error() string {
	return fmt.Sprintf("fence tampered for landing attempt %q: expected SHA %q or IDs %q / %q, got SHA %q ID %q; fence protection is missing",
		e.AttemptID, e.ExpectedFence, "land/"+e.AttemptID, "abandon/"+e.AttemptID, e.ActualFence, e.ActualFenceID)
}

func (x *Executor) resolve(ctx context.Context, a queue.ResolveLanding, emit func(queue.Event) error) error {
	for decision := 0; ; decision++ {
		sha, id, err := x.Git.ReadFence(ctx, x.Repo, x.FenceRef)
		if err != nil {
			return err
		}
		switch {
		case id == "land/"+a.AttemptID:
			return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: sha})
		case id == "abandon/"+a.AttemptID:
			return emit(queue.LandingAbandoned{BatchID: a.BatchID, NewFence: sha})
		case sha != a.FenceToken:
			return &ErrFenceTampered{AttemptID: a.AttemptID, ExpectedFence: a.FenceToken, ActualFence: sha, ActualFenceID: id}
		}
		next, err := x.Git.AdvanceFence(ctx, x.Repo, x.FenceRef, a.FenceToken, "abandon/"+a.AttemptID)
		if err == nil {
			return emit(queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next})
		}
		var rejected *forge.LandRejectedError
		if decision == 1 || !errors.As(err, &rejected) {
			return err
		}
	}
}

// Missing or nonterminal required checks keep the batch pending. For duplicate
// names, failure dominates pending, and pending dominates success.
func (x *Executor) checkResult(checks []forge.CheckRun) (terminal, success bool) {
	success = true
	terminal = true
	for _, required := range x.RequiredChecks {
		found, pending, failed := false, false, false
		for _, check := range checks {
			if check.Name == required {
				found = true
				switch check.State {
				case forge.Failure:
					failed = true
				case forge.Success:
				default:
					pending = true
				}
			}
		}
		if !found || pending && !failed {
			terminal = false
		}
		if failed {
			success = false
		}
	}
	return terminal, success
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}
