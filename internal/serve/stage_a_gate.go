package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// readStageABatch runs after the exact-head trust read. It never re-diffs at
// landing: configuration/reconciliation must already have refreshed evidence.
func (s *Server) readStageABatch(ctx context.Context, w *worker, state queue.State, land queue.Land) ([]queue.ApprovalEligibilityObserved, error) {
	result := make([]queue.ApprovalEligibilityObserved, 0, len(land.PRs))
	for _, pin := range land.PRs {
		r, _ := state.PRRevision(pin.PR)
		if r.HeadSHA != pin.HeadSHA {
			return nil, store.ErrLandingDecisionStale
		}
		c, ok := state.Classification(r.ChangeID, r.Revision)
		if !ok || c.Evidence.Classification.Inputs.Head != pin.HeadSHA {
			return nil, store.ErrLandingDecisionStale
		}
		forward, err := w.Git.IsAncestor(ctx, w.repo(), c.Evidence.Classification.Inputs.Base, state.BaseSHA)
		if err != nil {
			return nil, err
		}
		if !forward {
			return nil, store.ErrLandingDecisionStale
		}
		fresh, err := s.readStageAInputs(ctx, w, state, approvalBasis(r))
		if err != nil {
			return nil, err
		}
		result = append(result, fresh)
	}
	return result, nil
}

type testedMessageReader interface {
	ReadCommitMessage(context.Context, forge.RepoRef, string) (string, error)
}

func testedMessages(ctx context.Context, w *worker, land queue.Land) ([]string, error) {
	reader, ok := w.Git.(testedMessageReader)
	if !ok {
		return nil, store.LandingDecisionRequiredError{}
	}
	if land.Method == queue.FastForward {
		message, err := reader.ReadCommitMessage(ctx, w.repo(), land.TestedSHA)
		return []string{message}, err
	}
	history, ok := w.Git.(forge.CommitHistoryReader)
	if !ok {
		return nil, store.LandingDecisionRequiredError{}
	}
	commits, truncated, err := history.CommitsBetween(ctx, w.repo(), land.ExpectedBaseSHA, land.TestedSHA, forge.MaxAncestryCommits)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, errors.New("tested member history exceeds limit")
	}
	// Rebase writes each pair on the final replay commit for that member. Read
	// immutable objects, parse with 9b's reader, and select exactly one per pin.
	byID := map[string]string{}
	for _, sha := range commits {
		message, err := reader.ReadCommitMessage(ctx, w.repo(), sha)
		if err != nil {
			return nil, err
		}
		if !stringsContainChangeTrailer(message) {
			continue
		}
		members, err := forge.ParseChangeTrailers(message)
		if err != nil {
			return nil, err
		}
		if len(members) != 1 {
			return nil, errors.New("unexpected tested member metadata")
		}
		m := members[0]
		if _, exists := byID[m.ChangeID]; exists {
			return nil, errors.New("duplicate tested member")
		}
		byID[m.ChangeID] = message
	}
	messages := []string{}
	for _, p := range land.PRs {
		id, err := w.Key.ChangeID(p.PR)
		if err != nil {
			return nil, err
		}
		message, ok := byID[string(id)]
		if !ok {
			return nil, fmt.Errorf("tested metadata missing for %s", id)
		}
		messages = append(messages, message)
	}
	if len(messages) != len(byID) {
		return nil, errors.New("extra tested member")
	}
	return messages, nil
}

func stringsContainChangeTrailer(message string) bool {
	// Avoid treating contributor-only commits in a rebase as missing metadata.
	// Any reserved change key still goes through the strict shared parser.
	return strings.Contains(strings.ToLower(message), "openmerge-change") || strings.Contains(strings.ToLower(message), "openmerge-classification")
}

func (s *Server) stageAGate(w *worker, claim store.ClaimedAction, cfg queue.Config) *executor.LandingDecisionGate {
	policy := w.stageA.policy
	return &executor.LandingDecisionGate{Policy: policy,
		Load: func(ctx context.Context) (queue.State, error) {
			state, _, err := s.store.Load(ctx, w.id)
			return state, err
		},
		ReadInputs: func(ctx context.Context, land queue.Land, state queue.State) ([]queue.ApprovalEligibilityObserved, error) {
			if w.stageA.policy.Digest != policy.Digest {
				return nil, store.ErrLandingDecisionStale
			}
			fresh, err := s.readStageABatch(ctx, w, state, land)
			if err != nil {
				return nil, err
			}
			checks, err := w.Driver.CheckRuns(ctx, w.repo(), land.TestedSHA)
			if err != nil {
				return nil, err
			}
			for i, pin := range land.PRs {
				evidence, err := stageAReviewEvidence(state, policy, fresh[i])
				if err != nil {
					return nil, err
				}
				values, err := policyValuesWithApprovals(w.cfg.MergeWhen, w.prs[pin.PR], checks, evidence)
				if err != nil {
					return nil, err
				}
				met, _ := w.cfg.MergeWhen.Evaluate(values)
				if !met {
					return nil, executor.ErrChecksNotSuccessful
				}
			}
			return fresh, nil
		},
		ReadTestedMessages: func(ctx context.Context, land queue.Land) ([]string, error) { return testedMessages(ctx, w, land) },
		Commit: func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
			return s.store.StartLanding(ctx, claim, cfg, policy, fresh, decisions, start)
		},
		Reject: func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, expected []string, start queue.LandingStarted, rejection queue.ApprovalGateRejected) error {
			return s.store.RejectLanding(ctx, claim, cfg, policy, fresh, expected, start, rejection)
		},
	}
}

// stageACheckPolicy consumes approval-only waits BEFORE executor CI timers,
// retry, ChecksObserved or timeout handling. Genuine CI failure retains PR 8's
// full nested-expression outcome. Policy-only requirements wait at Land.
func (s *Server) stageACheckPolicy(ctx context.Context, w *worker, batch *queue.Batch, checks []forge.CheckRun) (bool, bool, error) {
	if batch == nil {
		return false, false, errors.New("no approval batch")
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return false, false, err
	}
	land := queue.Land{BatchID: batch.ID, TestedSHA: batch.TestedSHA, PRs: slices.Clone(batch.PRs)}
	fresh, err := s.readStageABatch(ctx, w, state, land)
	if err != nil {
		return false, false, err
	}
	pending, waiting := false, []int{}
	for i, pin := range batch.PRs {
		evidence, err := stageAReviewEvidence(state, w.stageA.policy, fresh[i])
		if err != nil {
			return false, false, err
		}
		outcome, err := mergeResultWithApprovals(w.cfg.MergeWhen, w.prs[pin.PR], checks, evidence)
		if err != nil {
			return false, false, err
		}
		if outcome.Terminal && !outcome.Success {
			return true, false, nil
		}
		if outcome.WaitingForApproval {
			waiting = append(waiting, i)
		}
		pending = pending || !outcome.Terminal
	}
	if len(waiting) == 0 {
		return !pending, !pending, nil
	}
	next := state
	events := []queue.Event{}
	for _, f := range fresh {
		events = append(events, f)
		next = queue.Apply(next, f)
	}
	rejection := queue.ApprovalGateRejected{BatchID: batch.ID, TestedSHA: batch.TestedSHA, PRs: slices.Clone(batch.PRs), Waiting: []queue.ApprovalWait{}}
	for _, i := range waiting {
		f := fresh[i]
		rejection.Waiting = append(rejection.Waiting, queue.ApprovalWait{Basis: f.Basis, Fingerprint: next.ApprovalFingerprint(f.Basis.ChangeID), Reasons: []string{"review-condition"}})
	}
	events = append(events, rejection)
	expected := []string{}
	for _, f := range fresh {
		expected = append(expected, state.ApprovalFingerprint(f.Basis.ChangeID))
	}
	data := []byte(`true`)
	err = s.store.RecordEvidence(ctx, w.id, "stage-a/check-wait/"+batch.ID, data, true, func(current queue.State) error {
		b := current.Batch(batch.ID)
		if b == nil || b.TestedSHA != batch.TestedSHA {
			return store.ErrLandingDecisionStale
		}
		for i, f := range fresh {
			if current.ApprovalFingerprint(f.Basis.ChangeID) != expected[i] {
				return store.ErrLandingDecisionStale
			}
		}
		return preflightStageA(current, events)
	}, events...)
	if err != nil {
		return false, false, err
	}
	return false, false, executor.ErrLandingApprovals
}
