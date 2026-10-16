// Package executor turns queue actions into forge calls and durable result events.
// The caller owns ADR 0002's watermark validation, locking, and action claims;
// Execute must only receive actions still authorized by that durable state.
package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Executor uses explicit dependencies so the same code runs with real or fake git.
// Nil Now uses time.Now; nil NewID generates a cryptographically random attempt.
// Callers must run Git.EnsureFence for "openmerge/fence/" + Base during setup
// and record its token before executing queue actions. Execute never bootstraps
// a missing fence during landing.
type Executor struct {
	Driver                forge.Driver
	Git                   forge.Git
	Repo                  forge.RepoRef
	Base                  string
	RequiredChecks        []string
	AllowNoRequiredChecks bool
	// AllowSkippedNeutral explicitly permits skipped and neutral required checks.
	AllowSkippedNeutral bool
	NewID               func() (string, error)
	Now                 func() time.Time
}

// Execute stops at the first failed emit. emit must durably record its event
// before returning nil. BatchStarted precedes test-ref construction, and
// LandingStarted precedes the git push;
// after it is recorded, the caller must only execute ResolveLanding for that
// batch, never retry Land. Its uncertain result is recovered from the fence.
// Infrastructure build errors emit TestRefBuildFailed and also return the error.
func (x *Executor) Execute(ctx context.Context, action queue.Action, emit func(queue.Event) error) error {
	if len(x.RequiredChecks) == 0 && !x.AllowNoRequiredChecks {
		return errors.New("required checks must be configured or AllowNoRequiredChecks explicitly enabled")
	}
	switch a := action.(type) {
	case queue.BuildTestRef:
		if err := emit(queue.BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: append([]queue.BatchPR(nil), a.PRs...), At: x.now()}); err != nil {
			return err
		}
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
	case queue.FailBatch:
		return emit(queue.BatchFailed(a))
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
		attemptID, err := x.newID()
		if err != nil {
			return err
		}
		if err := emit(queue.LandingStarted{BatchID: a.BatchID, AttemptID: attemptID, FenceToken: a.FenceToken, At: x.now()}); err != nil {
			return err
		}
		prs := make([]forge.BuildPR, len(a.PRs))
		for i, pr := range a.PRs {
			prs[i] = forge.BuildPR{Number: pr.PR, HeadSHA: pr.HeadSHA}
		}
		next, err := x.Git.Land(ctx, x.Repo, forge.LandRequest{
			Base: x.Base, ExpectedBaseSHA: a.ExpectedBaseSHA, TestedSHA: a.TestedSHA,
			Fence: x.fenceRef(), ExpectedFence: a.FenceToken, NextFenceID: "land/" + attemptID, PRs: prs,
		})
		if errors.Is(err, forge.ErrLeaseRejected) {
			return x.resolve(ctx, queue.ResolveLanding{BatchID: a.BatchID, AttemptID: attemptID, TestedSHA: a.TestedSHA, FenceToken: a.FenceToken}, emit)
		}
		if err != nil {
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
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, a.HeadSHA, forge.Status{State: forge.StatusState(a.Status), Reason: a.Reason, Summary: a.Detail}); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.MarkLanded:
		if err := x.Driver.MarkLanded(ctx, x.Repo, a.PR, a.LandedSHA); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.Dequeue:
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, a.HeadSHA, forge.Status{State: forge.Dequeued, Reason: string(a.Reason)}); err != nil {
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
	ExpectedTestedSHA, ActualTestedSHA                   string
}

func (e *ErrFenceTampered) Error() string {
	return fmt.Sprintf("fence tampered for landing attempt %q: expected SHA %q or IDs %q / %q, got SHA %q ID %q tested=%q (expected tested=%q); fence protection is missing",
		e.AttemptID, e.ExpectedFence, "land/"+e.AttemptID, "abandon/"+e.AttemptID, e.ActualFence, e.ActualFenceID, e.ActualTestedSHA, e.ExpectedTestedSHA)
}

func (x *Executor) resolve(ctx context.Context, a queue.ResolveLanding, emit func(queue.Event) error) error {
	for decision := 0; ; decision++ {
		sha, id, tested, err := x.Git.ReadFence(ctx, x.Repo, x.fenceRef())
		if err != nil {
			return err
		}
		switch {
		case id == "land/"+a.AttemptID && tested == a.TestedSHA:
			return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: sha})
		case id == "abandon/"+a.AttemptID:
			return emit(queue.LandingAbandoned{BatchID: a.BatchID, NewFence: sha})
		case sha != a.FenceToken || id == "land/"+a.AttemptID:
			return &ErrFenceTampered{AttemptID: a.AttemptID, ExpectedFence: a.FenceToken, ActualFence: sha, ActualFenceID: id, ExpectedTestedSHA: a.TestedSHA, ActualTestedSHA: tested}
		}
		next, err := x.Git.AdvanceFence(ctx, x.Repo, x.fenceRef(), a.FenceToken, "abandon/"+a.AttemptID)
		if err == nil {
			return emit(queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next})
		}
		if decision == 1 || !errors.Is(err, forge.ErrLeaseRejected) {
			return err
		}
	}
}

// Missing or unknown required checks keep the batch pending. Only the latest
// run of each name participates; cancelled is a failure until superseded.
func (x *Executor) checkResult(checks []forge.CheckRun) (terminal, success bool) {
	latest := make(map[string]forge.CheckRun)
	for _, check := range checks {
		old, ok := latest[check.Name]
		if !ok || check.ID >= old.ID {
			latest[check.Name] = check
		}
	}
	terminal, success = true, true
	for _, name := range x.RequiredChecks {
		check, ok := latest[name]
		if !ok {
			terminal = false
			continue
		}
		switch check.State {
		case forge.Success:
		case forge.Skipped, forge.Neutral:
			if !x.AllowSkippedNeutral {
				success = false
			}
		case forge.Failure, forge.Cancelled:
			success = false
		default:
			terminal = false
		}
	}
	return terminal, success
}

func (x *Executor) fenceRef() string { return "openmerge/fence/" + x.Base }

func (x *Executor) newID() (string, error) {
	if x.NewID != nil {
		id, err := x.NewID()
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", errors.New("landing attempt ID must not be empty")
		}
		return id, nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}
