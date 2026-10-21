package serve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// readStageABatch runs after the exact-head trust read. It never re-diffs at
// landing: configuration/reconciliation must already have refreshed evidence.
// It journals newly observed stances/commands; callers must reload the durable
// state before evaluating the returned eligibility against its stance fold.
func (s *Server) readStageABatch(ctx context.Context, w *worker, state queue.State, land queue.Land) ([]queue.ApprovalEligibilityObserved, error) {
	result := make([]queue.ApprovalEligibilityObserved, 0, len(land.PRs))
	for _, pin := range land.PRs {
		r, _ := state.PRRevision(pin.PR)
		if r.HeadSHA != pin.HeadSHA {
			return nil, store.ErrLandingDecisionStale
		}
		basis := approvalBasis(r)
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
		_, reader, err := w.stageReaders()
		if err != nil {
			return nil, err
		}
		native, err := reader.NativeReviews(ctx, w.repo(), pin.PR)
		if err != nil {
			return nil, err
		}
		// Acquire once, retain attributed sources, then evaluate their durable
		// fold. Commands use their normal immutable dedupe and receipt bindings.
		if err := s.observeStageANative(ctx, w, w.prs[pin.PR], basis, native); err != nil {
			return nil, err
		}
		if err := s.stageACommandsFromNative(ctx, w, pin.PR, native, true, basis.At); err != nil {
			return nil, err
		}
		current, _, err := s.store.Load(ctx, w.id)
		if err != nil {
			return nil, err
		}
		fresh, err := s.readStageAInputsFromNative(ctx, w, current, basis, native)
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
	acquirer, ok := w.Git.(forge.CommitAcquirer)
	if !ok {
		return nil, store.LandingDecisionRequiredError{}
	}
	if len(land.PRs) < 1 || len(land.PRs) > forge.MaxFirstParentCommits {
		return nil, errors.New("invalid tested acquisition member count")
	}
	// Fetch the tested tip first: strict servers may refuse a separate want
	// for its unadvertised base ancestor. Its graph includes that base.
	shas := []string{land.TestedSHA, land.ExpectedBaseSHA}
	for _, pin := range land.PRs {
		shas = append(shas, pin.HeadSHA)
	}
	release, err := acquirer.AcquireCommits(ctx, w.repo(), shas)
	if err != nil {
		return nil, fmt.Errorf("acquire tested commits: %w", err)
	}
	defer release()
	reader, ok := w.Git.(testedMessageReader)
	if !ok {
		return nil, store.LandingDecisionRequiredError{}
	}
	if land.Method == queue.FastForward {
		message, err := reader.ReadCommitMessage(ctx, w.repo(), land.TestedSHA)
		return []string{message}, err
	}
	history, ok := w.Git.(forge.FirstParentCommitReader)
	if !ok {
		return nil, store.LandingDecisionRequiredError{}
	}
	commits, err := testedMemberCommits(ctx, w, land, history)
	if err != nil {
		return nil, err
	}
	// Read and strictly parse only structurally selected, queue-generated member
	// commits. Contributor history can contain arbitrary reserved-looking text;
	// it must never influence parser acceptance or rejection at this boundary.
	byID := map[string]string{}
	for _, sha := range commits {
		message, err := reader.ReadCommitMessage(ctx, w.repo(), sha)
		if err != nil {
			return nil, err
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

// testedMemberCommits selects queue-generated commits through BuildTestRef's
// first-parent structure before their messages are read. This never walks
// contributor merge parents or performs a per-history-commit ancestry query.
// Merge and squash create one first-parent commit per member. Rebase creates
// one first-parent replay per source commit, so its member marker is the final
// replay in each known first-parent source segment.
func testedMemberCommits(ctx context.Context, w *worker, land queue.Land, history forge.FirstParentCommitReader) ([]string, error) {
	// BuildTestRef adds no fixed first-parent commits for these methods.
	// Rebase is the exception to one commit per member: count its exact source
	// segments first, under the composition reader's aggregate budget.
	limit := len(land.PRs)
	if limit < 1 || limit > forge.MaxFirstParentCommits {
		return nil, fmt.Errorf("tested member count %d exceeds first-parent limit [1, %d]", limit, forge.MaxFirstParentCommits)
	}
	var sourceLengths []int
	switch land.Method {
	case queue.Merge, queue.Squash:
	case queue.Rebase:
		sources, ok := w.Git.(forge.CommitHistoryReader)
		if !ok {
			return nil, store.LandingDecisionRequiredError{}
		}
		limit = 0
		for _, pin := range land.PRs {
			remaining := min(forge.MaxCommitHistoryCommits, forge.MaxFirstParentCommits-limit)
			if remaining < 1 {
				return nil, fmt.Errorf("tested source history exceeds first-parent limit %d", forge.MaxFirstParentCommits)
			}
			// BuildTestRef replays head ^ build-base. The build base may
			// have advanced beyond the contributor branch point.
			source, truncated, err := sources.CommitsBetween(ctx, w.repo(), land.ExpectedBaseSHA, pin.HeadSHA, remaining)
			if err != nil {
				return nil, err
			}
			if truncated {
				return nil, fmt.Errorf("tested source history for PR #%d exceeds remaining composition limit %d", pin.PR, remaining)
			}
			if len(source) == 0 {
				return nil, errors.New("unexpected rebase tested member structure")
			}
			sourceLengths = append(sourceLengths, len(source))
			limit += len(source)
		}
	default:
		return nil, errors.New("unsupported tested merge method")
	}
	commits, truncated, err := history.FirstParentCommits(ctx, w.repo(), land.ExpectedBaseSHA, land.TestedSHA, limit)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("tested member history exceeds composition limit %d for %d members", limit, len(land.PRs))
	}
	switch land.Method {
	case queue.Merge, queue.Squash:
		if len(commits) != len(land.PRs) {
			return nil, errors.New("unexpected tested member structure")
		}
		return commits, nil
	case queue.Rebase:
		// First-parent readers list newest first. BuildTestRef replays each
		// pinned source history in PR order, so reverse the generated chain and
		// select the final replay of each source segment without reading any
		// contributor message.
		slices.Reverse(commits)
		members := make([]string, 0, len(land.PRs))
		next := 0
		for _, length := range sourceLengths {
			next += length
			if next > len(commits) {
				return nil, errors.New("unexpected rebase tested member structure")
			}
			members = append(members, commits[next-1])
		}
		if next != len(commits) {
			return nil, errors.New("unexpected rebase tested member structure")
		}
		return members, nil
	default:
		return nil, errors.New("unsupported tested merge method")
	}
}

// stageAGate's explicitly enabled authority path checks local check-derived
// and available non-review evidence shape and size before ordinary policy
// handling, without spending approval-read quota. There, approved-reviews-by
// is known only after the final fresh approval read, so its list-driven overflow
// can surface only at final admission after successful CI and may be preempted
// by an approval-read or quota error.
func (s *Server) stageAGate(w *worker, claim store.ClaimedAction, cfg queue.Config, basis queue.State) *executor.LandingDecisionGate {
	policy := w.stageA.policy
	var acquisition *store.ApprovalRead
	var checkEvidence *queue.LandingChecksRecorded
	pin := basis.AuthorityPin()
	// AuthorityRequired is an explicit internal Target opt-in. A setup draft or
	// surviving journal pin cannot enable authority evidence on a public GitHub
	// target while config.Load still rejects authority activation.
	recordChecks := w.AuthorityRequired && (policy.RequirePolicyOwners || pin != nil && pin.Authority == "openmerge")
	gate := &executor.LandingDecisionGate{Policy: policy, ReloadAfterInputs: true,
		Load: func(ctx context.Context) (queue.State, error) {
			state, _, err := s.store.Load(ctx, w.id)
			return state, err
		},
		ReadInputsWithChecks: func(ctx context.Context, land queue.Land, state queue.State, checks []forge.CheckRun, checksAt time.Time, attempt string) ([]queue.ApprovalEligibilityObserved, error) {
			if w.stageA.policy.Digest != policy.Digest {
				return nil, store.ErrLandingDecisionStale
			}
			members := make([]queue.ChangeID, 0, len(land.PRs))
			for _, pin := range land.PRs {
				r, _ := state.PRRevision(pin.PR)
				members = append(members, r.ChangeID)
			}
			var err error
			acquisition, err = s.store.BeginApprovalRead(ctx, w.id, members)
			if err != nil {
				return nil, err
			}
			ctx = acquisition.Context(ctx)
			fresh, err := s.readStageABatch(ctx, w, state, land)
			if err != nil {
				return nil, err
			}
			// Native stances and authenticated commands from the final read are
			// durable before this evaluation. The executor reloads again before
			// preparing decisions; StartLanding rederives under the queue lock.
			state, _, err = s.store.Load(ctx, w.id)
			if err != nil {
				return nil, err
			}
			// Authority uses the executor's exact retry-enriched, provenance-checked
			// read. Legacy approval queues retain their existing fresh-read path.
			if !recordChecks {
				checks, err = w.Driver.CheckRuns(ctx, w.repo(), land.TestedSHA)
				if err != nil {
					return nil, err
				}
			}
			var builder *queue.LandingChecksBuilder
			if recordChecks {
				builder, err = landingCheckEvidence(basis, policy.Digest, land, checks, checksAt, attempt)
				if err != nil {
					return nil, err
				}
			}
			checksMet := true
			for i, pin := range land.PRs {
				evidence, err := stageAReviewEvidence(state, policy, fresh[i])
				if err != nil {
					return nil, err
				}
				values, err := policyValuesWithApprovals(w.cfg.MergeWhen, w.prs[pin.PR], checks, evidence)
				if err != nil {
					return nil, err
				}
				if recordChecks {
					if err := builder.RecordMember(pin.PR, pin.HeadSHA, w.cfg.MergeWhen, values); err != nil {
						return nil, err
					}
				}
				met, _ := w.cfg.MergeWhen.Evaluate(values)
				if !recordChecks && !met {
					return nil, executor.ErrChecksNotSuccessful
				}
				checksMet = checksMet && met
			}
			if !checksMet {
				return nil, executor.ErrChecksNotSuccessful
			}
			if recordChecks {
				// Final authority admission is independent of the observer's
				// cadence and cannot be bypassed by omitting its resolver.
				current, err := s.readAuthorityAdmission(ctx, w, basis)
				if err != nil {
					return nil, err
				}
				e := builder.Event()
				e.RequiredCheckSource = current.LandingCheckSources()
				checkEvidence = &e
			}
			return fresh, nil
		},
		ReadTestedMessages: func(ctx context.Context, land queue.Land) ([]string, error) { return testedMessages(ctx, w, land) },
		Commit: func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, decisions []queue.LandingDecisionRecorded, start queue.LandingStarted) error {
			if acquisition == nil {
				return store.ErrLandingDecisionStale
			}
			if recordChecks {
				if checkEvidence == nil {
					return store.ErrLandingDecisionStale
				}
				if checkEvidence.AttemptID != start.AttemptID {
					return store.ErrLandingDecisionStale
				}
				return s.store.StartLanding(acquisition.Context(ctx), claim, cfg, policy, fresh, decisions, start, *checkEvidence)
			}
			return s.store.StartLanding(acquisition.Context(ctx), claim, cfg, policy, fresh, decisions, start)
		},
		Reject: func(ctx context.Context, fresh []queue.ApprovalEligibilityObserved, expected []string, start queue.LandingStarted, rejection queue.ApprovalGateRejected) error {
			if acquisition == nil {
				return store.ErrLandingDecisionStale
			}
			return s.store.RejectLanding(acquisition.Context(ctx), claim, cfg, policy, fresh, expected, start, rejection)
		},
	}
	if recordChecks {
		gate.ValidateChecks = func(_ context.Context, land queue.Land, checks []forge.CheckRun, at time.Time, attempt string) error {
			// This preflight must not read approvals, journal stances, or acquire an
			// approval read on failing/pending CI. Final admission rebuilds complete
			// evidence from fresh approvals.
			builder, err := landingCheckEvidence(basis, policy.Digest, land, checks, at, attempt)
			if err != nil {
				return err
			}
			for _, pin := range land.PRs {
				pr := w.prs[pin.PR]
				// This only avoids building a large cached review list; deleting the
				// review attribute below is what makes reviews unknown in preflight.
				pr.Approvals = nil
				values, err := policyValues(w.cfg.MergeWhen, pr, checks)
				if err != nil {
					return err
				}
				// Reviews are unknown here, never an authorization result.
				delete(values, reviewAttribute)
				if err := builder.RecordMember(pin.PR, pin.HeadSHA, w.cfg.MergeWhen, values); err != nil {
					return err
				}
			}
			return nil
		}
	} else {
		read := gate.ReadInputsWithChecks
		gate.ReadInputs = func(ctx context.Context, land queue.Land, state queue.State) ([]queue.ApprovalEligibilityObserved, error) {
			return read(ctx, land, state, nil, time.Time{}, "")
		}
		gate.ReadInputsWithChecks = nil
	}
	return gate
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
	members := make([]queue.ChangeID, 0, len(batch.PRs))
	for _, pin := range batch.PRs {
		r, _ := state.PRRevision(pin.PR)
		members = append(members, r.ChangeID)
	}
	acquisition, err := s.store.BeginApprovalRead(ctx, w.id, members)
	if err != nil {
		return false, false, err
	}
	ctx = acquisition.Context(ctx)
	fresh, err := s.readStageABatch(ctx, w, state, land)
	if err != nil {
		return false, false, err
	}
	// readStageABatch may journal fresh native stances and approval commands.
	// Evaluate the approval condition, fingerprints, and wait against that
	// committed projection rather than the pre-journal snapshot.
	state, _, err = s.store.Load(ctx, w.id)
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
