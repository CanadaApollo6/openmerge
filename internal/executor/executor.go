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
	"slices"
	"strings"
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
	// DisableDeleteHeadBranch retains landed heads; default false enables cleanup.
	DisableDeleteHeadBranch bool
	// CheckPolicy evaluates repository merge conditions on the tested SHA.
	// Nil retains the explicit RequiredChecks policy.
	CheckPolicy func([]forge.CheckRun) (terminal, success bool, err error)
	// ReferencesCheck selects names relevant to CheckPolicy diagnostics. Nil uses RequiredChecks.
	ReferencesCheck func(string) bool
	// PreviousUnboundChecks suppresses unchanged diagnostic observations.
	PreviousUnboundChecks []queue.UnboundCheck
	// ChecksTimeoutDetail supplies durable provenance context for a timeout dequeue.
	ChecksTimeoutDetail string
	// BeforeLand freshly revalidates PR trust after the final checks read.
	BeforeLand func(context.Context, queue.Land) error
	NewID      func() (string, error)
	Now        func() time.Time
}

// Execute stops at the first failed emit. emit must durably record its event
// before returning nil. BatchStarted precedes test-ref construction, and
// Land freshly requires terminal-success checks before recording LandingStarted,
// which precedes the git push;
// after it is recorded, the caller must only execute ResolveLanding for that
// batch, never retry Land. Its uncertain result is recovered from the fence.
// Before recording Landed or closing a PR with MarkLanded, Execute freshly reads
// the base and verifies that it contains the landed commit (the pinned head for
// fast-forward, otherwise the tested SHA). ErrLandingNotOnBase
// leaves the batch in Landing without a result event and recurs on resolution
// retries while the commit remains absent. A failed MarkLanded stays unacknowledged.
// Infrastructure build errors emit TestRefBuildFailed and also return the error.
func (x *Executor) Execute(ctx context.Context, action queue.Action, emit func(queue.Event) error) error {
	if len(x.RequiredChecks) == 0 && !x.AllowNoRequiredChecks && x.CheckPolicy == nil {
		return errors.New("required checks must be configured or AllowNoRequiredChecks explicitly enabled")
	}
	switch a := action.(type) {
	case queue.BuildTestRef:
		if err := emit(queue.BatchStarted{BatchID: a.BatchID, BaseSHA: a.BaseSHA, PRs: append([]queue.BatchPR(nil), a.PRs...), At: x.now()}); err != nil {
			return err
		}
		if a.Method == queue.FastForward {
			return x.buildFastForward(ctx, a, emit)
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
		if err := x.observeCheckProvenance(checks, a.BatchID, a.TestedSHA, emit); err != nil {
			return err
		}
		terminal, success, ambiguity := x.checkResult(checks, a.BatchID)
		if terminal {
			if err := emit(queue.ChecksCompleted{TestedSHA: a.TestedSHA, Success: success}); err != nil {
				return err
			}
		} else if err := emit(queue.ChecksReopened{TestedSHA: a.TestedSHA, At: at}); err != nil {
			return err
		}
		if err := emit(queue.ChecksRefreshed{BatchID: a.BatchID, TestedSHA: a.TestedSHA, At: at}); err != nil {
			return err
		}
		return ambiguity
	case queue.Land:
		checks, err := x.Driver.CheckRuns(ctx, x.Repo, a.TestedSHA)
		if err != nil {
			return err
		}
		if err := x.observeCheckProvenance(checks, a.BatchID, a.TestedSHA, emit); err != nil {
			return err
		}
		terminal, success, ambiguity := x.checkResult(checks, a.BatchID)
		if !terminal || !success {
			at := x.now()
			var result queue.Event = queue.ChecksReopened{TestedSHA: a.TestedSHA, At: at}
			if terminal {
				result = queue.ChecksCompleted{TestedSHA: a.TestedSHA, Success: false}
			}
			if err := emit(result); err != nil {
				return err
			}
			if !terminal {
				if err := emit(queue.ChecksRefreshed{BatchID: a.BatchID, TestedSHA: a.TestedSHA, At: at}); err != nil {
					return err
				}
			}
			if ambiguity != nil {
				return ambiguity
			}
			return ErrChecksNotSuccessful
		}
		if x.BeforeLand != nil {
			if err := x.BeforeLand(ctx, a); err != nil {
				return err
			}
		}
		landedSHA, fastForwardHead := a.TestedSHA, ""
		if a.Method == queue.FastForward {
			if len(a.PRs) == 0 {
				return errors.New("fast-forward requires pinned heads")
			}
			fastForwardHead = a.PRs[len(a.PRs)-1].HeadSHA
			if err := x.Git.VerifyFastForward(ctx, x.Repo, a.ExpectedBaseSHA, a.TestedSHA, fastForwardHead); err != nil {
				return err
			}
			landedSHA = fastForwardHead
		}
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
			Fence: x.fenceRef(), ExpectedFence: a.FenceToken, NextFenceID: "land/" + attemptID, PRs: prs, FastForwardHeadSHA: fastForwardHead,
		})
		if errors.Is(err, forge.ErrLeaseRejected) {
			return x.resolveLanding(ctx, queue.ResolveLanding{BatchID: a.BatchID, AttemptID: attemptID, TestedSHA: a.TestedSHA, FenceToken: a.FenceToken}, landedSHA, emit)
		}
		if err != nil {
			return err
		}
		if err := x.verifyLanding(ctx, landedSHA, attemptID); err != nil {
			return err
		}
		if err := emit(queue.HeadCleanupRequested{BatchID: a.BatchID, LandedSHA: landedSHA}); err != nil {
			return err
		}
		return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: landedSHA, NewFence: next})
	case queue.FastForwardResolveLanding:
		return x.resolveLanding(ctx, a.ResolveLanding, a.HeadSHA, emit)
	case queue.ResolveLanding:
		return x.resolve(ctx, a, emit)
	case queue.DeleteRef:
		if err := x.Driver.DeleteRef(ctx, x.Repo, a.Ref); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.DeleteHeadBranch:
		if !x.DisableDeleteHeadBranch {
			if err := x.verifyLanding(ctx, a.LandedSHA, ""); err != nil {
				return err
			}
			branch, err := x.Driver.HeadBranchForDeletion(ctx, x.Repo, a.PR, a.HeadSHA, x.Base)
			if err != nil {
				return err
			}
			if branch != "" {
				if err := x.Git.DeleteBranch(ctx, x.Repo, branch, a.HeadSHA); err != nil {
					return err
				}
			}
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.ReportStatus:
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, a.HeadSHA, forge.Status{State: forge.StatusState(a.Status), Reason: a.Reason, Summary: a.Detail}); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.MarkLanded:
		if err := x.verifyLanding(ctx, a.LandedSHA, ""); err != nil {
			return err
		}
		if err := x.Driver.MarkLanded(ctx, x.Repo, a.PR, a.LandedSHA); err != nil {
			return err
		}
		return emit(queue.ActionCompleted{Key: a.Key()})
	case queue.Dequeue:
		detail := dequeueSummary(a.Reason)
		if a.Reason == queue.ChecksTimeout && x.ChecksTimeoutDetail != "" {
			detail = x.ChecksTimeoutDetail
		}
		if err := x.Driver.ReportStatus(ctx, x.Repo, a.PR, a.HeadSHA, forge.Status{State: forge.Dequeued, Reason: string(a.Reason), Summary: detail}); err != nil {
			return err
		}
		return emit(queue.Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})
	default:
		return fmt.Errorf("unsupported queue action %T", action)
	}
}

// ErrChecksNotSuccessful leaves Land unacknowledged so a later passing rerun can
// plan the same action key again. Replan before retrying; no attempt was started.
var ErrChecksNotSuccessful = errors.New("required checks are no longer successful")

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
	return x.resolveLanding(ctx, a, a.TestedSHA, emit)
}

func (x *Executor) resolveLanding(ctx context.Context, a queue.ResolveLanding, landedSHA string, emit func(queue.Event) error) error {
	for decision := 0; ; decision++ {
		sha, id, tested, err := x.Git.ReadFence(ctx, x.Repo, x.fenceRef())
		if err != nil {
			return err
		}
		switch {
		case id == "land/"+a.AttemptID && tested == a.TestedSHA:
			if err := x.verifyLanding(ctx, landedSHA, a.AttemptID); err != nil {
				return err
			}
			if err := emit(queue.HeadCleanupRequested{BatchID: a.BatchID, LandedSHA: landedSHA}); err != nil {
				return err
			}
			return emit(queue.Landed{BatchID: a.BatchID, NewBaseSHA: landedSHA, NewFence: sha})
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

// CheckSourceAmbiguityError keeps required checks pending and gives the server
// the conflicting producers to surface. No terminal check result is emitted.
type CheckSourceAmbiguityError struct {
	Name    string
	Sources []string
}

func (e *CheckSourceAmbiguityError) Error() string {
	return fmt.Sprintf("required check %q has ambiguous sources %q; use distinct check names", e.Name, e.Sources)
}

// Missing, unknown, or ambiguous required checks keep the batch pending. Only
// the newest run within one source participates; cancelled counts as failure.
func (x *Executor) checkResult(checks []forge.CheckRun, batchID string) (terminal, success bool, ambiguity error) {
	// Batch refs are deterministic, including for persisted RefreshChecks actions.
	// Filter before either policy compares run IDs or checks source ambiguity.
	ref := "openmerge/queue/" + x.Base + "/" + batchID
	checks = slices.DeleteFunc(slices.Clone(checks), func(check forge.CheckRun) bool {
		return !strings.HasPrefix(check.Source, "status:") && check.HeadBranch != ref
	})
	if x.CheckPolicy != nil {
		return x.CheckPolicy(checks)
	}
	latest := make(map[string]forge.CheckRun)
	sources := make(map[string]map[string]bool)
	for _, check := range checks {
		if sources[check.Name] == nil {
			sources[check.Name] = make(map[string]bool)
		}
		sources[check.Name][check.Source] = true
		old, ok := latest[check.Name]
		if !ok || check.ID >= old.ID {
			latest[check.Name] = check
		}
	}
	terminal, success = true, true
	for _, name := range x.RequiredChecks {
		if len(sources[name]) > 1 || sources[name][""] {
			terminal = false
			if ambiguity == nil {
				var names []string
				for source := range sources[name] {
					names = append(names, source)
				}
				slices.Sort(names)
				ambiguity = &CheckSourceAmbiguityError{Name: name, Sources: names}
			}
			continue
		}
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
	return terminal, success, ambiguity
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
