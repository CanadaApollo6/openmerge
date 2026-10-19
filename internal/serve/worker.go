package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var errTrustGate = errors.New("PR no longer passes the exact-head trust gate")

func (s *Server) trustGate(ctx context.Context, w *worker, prs []queue.BatchPR) error {
	if err := s.configuration(ctx, w); err != nil {
		return err
	}
	if err := s.repositoryRules(ctx, w); err != nil {
		return err
	}
	if w.configError != nil {
		return errTrustGate
	}
	for _, pin := range prs {
		pr, err := s.observeWithClassification(ctx, w, pin.PR, false)
		if err != nil {
			return err
		}
		if pr.HeadSHA != pin.HeadSHA || !trusted(pr) || pr.Closed || pr.Base != w.Key.BaseBranch {
			return errTrustGate
		}
	}
	return nil
}

func (s *Server) execute(ctx context.Context, w *worker, claim store.ClaimedAction) (result error) {
	completion := store.Done
	// Completion survives shutdown cancellation. Uncertain writes are retried via
	// their durable idempotency keys or resolved through the landing fence.
	defer func() {
		if result != nil {
			completion = store.Failed
		}
		persist, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.store.Complete(persist, claim, completion); err != nil && !errors.Is(err, store.ErrClaimLost) {
			result = errors.Join(result, err)
		}
	}()
	actionCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	defer cancel()
	switch action := claim.Action.(type) {
	case queue.ReportStatus:
		if time.Now().Before(w.reportingNext) {
			return store.ErrActionDeferred
		}
		actionCtx = forge.WithRequestClass(actionCtx, forge.ReportingRequest)
	case queue.BuildChainedTestRef:
		if err := s.trustGate(actionCtx, w, action.PRs); err != nil && !errors.Is(err, errTrustGate) {
			return err
		}
	case queue.BuildTestRef:
		if err := s.trustGate(actionCtx, w, action.PRs); err != nil && !errors.Is(err, errTrustGate) {
			return err
		}
	case queue.Land:
		if err := s.trustGate(actionCtx, w, action.PRs); err != nil && !errors.Is(err, errTrustGate) {
			return err
		}
	}
	if w.managed && !w.ready {
		switch claim.Action.(type) {
		case queue.BuildChainedTestRef, queue.BuildTestRef, queue.Land:
			return store.ErrActionDeferred
		}
	}
	cfg := w.plannerConfig(s.options.PollInterval)
	if store.Consequential(claim.Action) {
		proceed, err := s.store.BeginConsequential(actionCtx, claim, cfg, time.Now())
		if err != nil || !proceed {
			return err
		}
	}
	state, _, err := s.store.Load(actionCtx, w.id)
	if err != nil {
		return err
	}
	if !store.Consequential(claim.Action) {
		current := false
		for _, action := range queue.Plan(cfg, state, time.Now()) {
			if action.Key() == claim.Key {
				current = true
				break
			}
		}
		if !current {
			if queue.DeferredByPause(cfg, state, claim.Action, time.Now()) {
				return store.ErrActionDeferred
			}
			completion = store.Cancelled
			return nil
		}
	}
	disableDeleteHeadBranch := s.options.DisableDeleteHeadBranch
	if w.deleteHeadBranch != nil {
		disableDeleteHeadBranch = !*w.deleteHeadBranch
	}
	if _, cleanup := claim.Action.(queue.DeleteHeadBranch); cleanup {
		// The queue schedules cleanup only from the attempt's recorded policy.
		disableDeleteHeadBranch = false
	}
	x := executor.Executor{Driver: w.Driver, Git: w.Git, Repo: w.repo(), Base: w.Key.BaseBranch, DisableDeleteHeadBranch: disableDeleteHeadBranch}
	batch := state.ActionBatch(claim.Action)
	if w.stageA != nil {
		x.RebaseComposition = func(ctx context.Context, build queue.BuildTestRef) (queue.RebaseCompositionObserved, error) {
			return readRebaseComposition(ctx, w, build, forge.MaxFirstParentCommits)
		}
		x.BuildMetadataState, x.BuildMetadataKey, x.BuildPolicyDigest = state, w.Key, w.stageA.policy.Digest
		x.BuildMetadata = func(_ queue.State, key executor.BuildChangeKey, b queue.Batch, digest string) ([]*forge.ChangeMetadata, error) {
			current, _, err := s.store.Load(actionCtx, w.id)
			if err != nil {
				return nil, err
			}
			recorded := current.Batch(b.ID)
			if recorded == nil {
				return nil, errors.New("build start was not recorded")
			}
			return executor.LoadBuildMetadataWithAncestry(current, key, *recorded, digest, func(ancestor, descendant string) (bool, error) {
				return w.Git.IsAncestor(actionCtx, w.repo(), ancestor, descendant)
			})
		}
		x.LandingDecisions = s.stageAGate(w, claim, cfg)
	}
	x.Speculative = cfg.MaxDepth() > 1
	x.ChecksRetryFailed = cfg.RetryLimit()
	x.CheckTimeout = cfg.CheckTimeout
	if batch != nil {
		x.StartedTestingAt = batch.StartedTestingAt
		x.CheckRetry = batch.CheckRetry
		x.PreviousObservedRuns = batch.LastObservedRuns
	}
	if batch != nil {
		x.PreviousUnboundChecks = state.BatchUnboundChecks(batch.ID)
	}
	x.ReferencesCheck = w.cfg.MergeWhen.ReferencesCheck
	if cfg.RetryLimit() == 1 {
		x.RetryFailure = func(checks []forge.CheckRun, name string) bool {
			if batch == nil {
				return false
			}
			prs := make([]forge.PullRequest, 0, len(batch.PRs))
			for _, pin := range batch.PRs {
				prs = append(prs, w.prs[pin.PR])
			}
			if w.stageA != nil {
				for _, pr := range prs {
					current, _, err := s.store.Load(actionCtx, w.id)
					if err != nil {
						return false
					}
					r, _ := current.PRRevision(pr.Number)
					fresh, err := s.readStageAInputs(actionCtx, w, current, approvalBasis(r))
					if err != nil {
						return false
					}
					evidence, err := stageAReviewEvidence(current, w.stageA.policy, fresh)
					if err != nil {
						return false
					}
					if retryPolicyFailureWithApprovals(w.cfg.MergeWhen, []forge.PullRequest{pr}, checks, name, evidence) {
						return true
					}
				}
				return false
			}
			return retryPolicyFailure(w.cfg.MergeWhen, prs, checks, name)
		}
	}
	if dequeue, ok := claim.Action.(queue.Dequeue); ok {
		x.ChecksTimeoutDetail = state.DequeueDetail(dequeue.PR, dequeue.Reason)
	}
	var finalChecks []forge.CheckRun
	x.CheckPolicy = func(checks []forge.CheckRun) (bool, bool, error) {
		finalChecks = checks
		if w.configError != nil {
			return false, false, errors.New("invalid repository configuration")
		}
		if batch == nil {
			return false, false, errors.New("no active batch for check policy")
		}
		if w.stageA != nil {
			return s.stageACheckPolicy(actionCtx, w, batch, checks)
		}
		pending := false
		for _, pin := range batch.PRs {
			terminal, met, err := mergeResult(w.cfg.MergeWhen, w.prs[pin.PR], checks)
			if err != nil {
				return false, false, err
			}
			if terminal && !met {
				return true, false, nil
			}
			pending = pending || !terminal
		}
		return !pending, !pending, nil
	}
	if w.stageA != nil {
		x.CheckPolicyOutcome = func(checks []forge.CheckRun) (executor.CheckOutcome, error) {
			terminal, success, err := s.stageACheckPolicy(actionCtx, w, batch, checks)
			if errors.Is(err, executor.ErrLandingApprovals) {
				return executor.CheckOutcome{ApprovalWait: true}, err
			}
			return executor.CheckOutcome{Terminal: terminal, Success: success}, err
		}
	}
	x.BeforeLand = func(ctx context.Context, land queue.Land) error {
		if err := s.trustGate(ctx, w, land.PRs); err != nil {
			return err
		}
		if w.stageA != nil {
			return nil
		} // full condition revalidated by stageAGate
		for _, pin := range land.PRs {
			met, err := evaluate(w.cfg.MergeWhen, w.prs[pin.PR], finalChecks)
			if err != nil {
				return err
			}
			if !met {
				return executor.ErrChecksNotSuccessful
			}
		}
		return nil
	}
	emit := s.store.Emit(actionCtx, claim, cfg)
	if w.stageA != nil {
		emit = s.store.EmitWithPolicy(actionCtx, claim, cfg, w.stageA.policy)
	}
	executionAction := claim.Action
	if report, ok := executionAction.(queue.ReportStatus); ok {
		summary, err := s.ledgerSummary(actionCtx, w, report.PR)
		if err != nil {
			return err
		}
		report.Detail += summary
		if w.stageA != nil {
			detail, err := s.stageACheckSummary(actionCtx, w, report.PR)
			if err != nil {
				return err
			}
			report.Detail += detail
		}
		executionAction = report
	}
	err = x.Execute(actionCtx, executionAction, func(event queue.Event) error {
		if err := emit(event); err != nil {
			return err
		}
		s.logCommitted(actionCtx, w)
		return nil
	})
	if err != nil {
		if deferred, ok := queue.DeferCheckCancellation(claim.Action, time.Now(), err.Error()); ok {
			persist, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return s.store.Emit(persist, claim, cfg)(deferred)
		}
	}
	var absent *executor.ErrLandingNotOnBase
	var ambiguity *executor.CheckSourceAmbiguityError
	if errors.As(err, &absent) {
		if pauseErr := s.block(actionCtx, w, queue.LandingNotOnBase, absent.Error(), absent.TestedSHA); pauseErr != nil {
			return errors.Join(err, pauseErr)
		}
	} else if errors.As(err, &ambiguity) {
		if blockErr := s.block(actionCtx, w, queue.CheckSources, ambiguity.Error(), ""); blockErr != nil {
			return errors.Join(err, blockErr)
		}
		// The independent recovery observer re-reads producers while starts stop.
		if reportErr := s.reportBatch(actionCtx, w, batch, forge.Status{State: forge.Blocked, Reason: "ambiguous-check-sources", Summary: ambiguity.Error()}); reportErr != nil {
			return errors.Join(err, reportErr)
		}
		return nil // refresh events committed; the next epoch re-reads CI
	}
	if _, reporting := claim.Action.(queue.ReportStatus); reporting && err != nil {
		var quota interface{ RetryAfter() time.Time }
		if errors.As(err, &quota) {
			w.reportingNext = quota.RetryAfter()
			return store.ErrActionDeferred
		}
	}
	if errors.Is(err, store.ErrLandingDecisionStale) {
		// The atomic start rolled back without cancelling the claim. Release it
		// for a fresh gate read rather than recording this Land action as done.
		return err
	}
	if errors.Is(err, store.ErrStale) {
		return nil
	}
	if err != nil {
		_ = s.report(actionCtx, w)
	}
	return err
}

func (s *Server) status(ctx context.Context, w *worker, pr int, head string, status forge.Status) error {
	if time.Now().Before(w.reportingNext) {
		return nil
	}
	ctx = forge.WithRequestClass(ctx, forge.ReportingRequest)
	summary, err := s.ledgerSummary(ctx, w, pr)
	if err != nil {
		return err
	}
	status.Summary += summary
	if w.stageA != nil {
		detail, err := s.stageACheckSummary(ctx, w, pr)
		if err != nil {
			return err
		}
		status.Summary += detail
	}
	key := fmt.Sprintf("status/%d", pr)
	value := struct {
		Head   string
		Status forge.Status
	}{head, status}
	// The durable cache is updated only after the idempotent forge write succeeds.
	previous, err := s.store.Observation(ctx, w.id, key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if string(previous) == string(data) {
		return nil
	}
	if err := w.Driver.ReportStatus(ctx, w.repo(), pr, head, status); err != nil {
		var rate interface{ RetryAfter() time.Time }
		if errors.As(err, &rate) {
			w.reportingNext = rate.RetryAfter()
			s.options.Logger.Debug("status reporting deferred", "repository", w.Key.Repository, "until", rate.RetryAfter())
			return nil
		}
		return err
	}
	return s.store.Record(ctx, w.id, key, data)
}

func (s *Server) reportBatch(ctx context.Context, w *worker, batch *queue.Batch, status forge.Status) error {
	if batch == nil {
		return nil
	}
	for _, pr := range batch.PRs {
		if err := s.status(ctx, w, pr.PR, pr.HeadSHA, status); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) report(ctx context.Context, w *worker) error {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if w.managed && !w.ready {
		for n, pr := range w.prs {
			if !pr.Closed {
				if err := s.status(ctx, w, n, pr.HeadSHA, w.waitingStatus()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	reason := state.PauseReason(w.plannerConfig(s.options.PollInterval))
	if reason != "" {
		var reasons, details []string
		blocks := state.Blocks(w.plannerConfig(s.options.PollInterval))
		for _, block := range blocks {
			reasons = append(reasons, block.Reason)
			details = append(details, block.Reason+" ["+string(block.Source)+"]: "+block.Detail)
		}
		status := forge.Status{State: forge.Paused, Reason: strings.Join(reasons, ", "), Summary: strings.Join(details, "; ") + "; " + queue.BlockRecovery(blocks)}
		if detail := state.CheckProvenanceDetail(); detail != "" {
			status.Reason += ", unbound-check-provenance"
			status.Summary += "; " + detail
		}
		for n, pr := range w.prs {
			if pr.Closed {
				continue
			}
			if err := s.status(ctx, w, n, pr.HeadSHA, status); err != nil {
				return err
			}
		}
		return nil
	}
	for _, entry := range state.Entries {
		status := forge.Status{State: forge.Queued, Summary: "Waiting for a test batch"}
		if batch := state.BatchForPR(entry.PR); batch != nil {
			status = forge.Status{State: forge.StatusState(batch.Phase), Summary: "Batch " + batch.ID}
			if batch.Speculative {
				ahead := 0
				for i, b := range state.Batches() {
					if b.ID == batch.ID {
						ahead = i
						break
					}
				}
				status.Summary += fmt.Sprintf("; chain position %d", batch.ChainPosition)
				if batch.ParentBatchID != "" {
					status.Summary += ", built on " + batch.ParentBatchID
				}
				status.Summary += fmt.Sprintf("; %d batches ahead", ahead)
			}
			provisional := batch.ProvisionalFailure
			if retry := batch.CheckRetry; retry != nil {
				status.Summary += "; " + checkRetryStatus(retry)
				if provisional == "" && !retry.Confirmed && retry.RerunState == "failed" {
					provisional = queue.ChecksFailed
				}
			}
			if provisional != "" {
				status.Summary += "; provisional failure: " + string(provisional)
			}
			if batch.InvalidationReason != "" {
				status.Summary += "; invalidated: " + batch.InvalidationReason
			}
			if status.State == "building" {
				status.State = forge.Testing
			}
			if status.State == forge.Testing || status.Reason == "ambiguous-check-sources" {
				if detail := state.BatchCheckProvenanceDetail(batch.ID); detail != "" {
					status = forge.Status{State: forge.Blocked, Reason: "unbound-check-provenance", Summary: detail}
				}
			}
		}
		if err := s.status(ctx, w, entry.PR, entry.HeadSHA, status); err != nil {
			return err
		}
	}
	return nil
}

func checkRetryStatus(retry *queue.CheckRetryState) string {
	if retry == nil {
		return ""
	}
	state := retry.RerunState
	if state == "" {
		state = "observed"
	}
	if state == "requested" && retry.Dispatched {
		state = "running"
	}
	detail := "check retry " + state
	if retry.Detail != "" {
		detail += ": " + retry.Detail
	}
	if state == "passed" && len(retry.FailedRuns) > 0 {
		detail += fmt.Sprintf(" (%d flaky original", len(retry.FailedRuns))
		if identities := checkRunIdentities(retry.FailedRuns); identities != "" {
			detail += ": " + identities
		}
		detail += ")"
	}
	return detail
}

func checkRunIdentities(runs []queue.CheckRun) string {
	identities := make([]string, 0, len(runs))
	for _, run := range runs {
		identity := fmt.Sprintf("%s/%s#%d.%d", run.Name, run.Source, run.ID, run.Attempt)
		if run.WorkflowRunID > 0 {
			identity += fmt.Sprintf(" workflow=%d %s", run.WorkflowRunID, run.WorkflowRunURL)
		}
		if run.URL != "" {
			identity += " job=" + run.URL
		}
		identities = append(identities, identity)
	}
	return strings.Join(identities, ", ")
}

func (w *worker) readinessReason() string {
	if w.lifecycleState != "" && w.lifecycleState != "active" {
		if w.lifecycleDetail != "" {
			return w.lifecycleDetail
		}
		return "queue is " + w.lifecycleState
	}
	if w.waiting != "" {
		return w.waiting
	}
	return "validation pending"
}

func (w *worker) waitingStatus() forge.Status {
	reason := "validation-pending"
	if w.lifecycleState != "" && w.lifecycleState != "active" {
		reason = w.lifecycleState
	}
	return forge.Status{State: forge.Paused, Reason: reason, Summary: w.readinessReason()}
}

func (s *Server) reportWaiting(ctx context.Context, w *worker) error {
	summaries, err := w.Driver.ListOpenPullRequests(ctx, w.repo(), w.Key.BaseBranch)
	if err != nil {
		return err
	}
	w.diagnosticHeads = make(map[int]string, len(summaries))
	for _, pr := range summaries {
		w.diagnosticHeads[pr.Number] = pr.HeadSHA
		// Diagnostic heads never replace complete policy observations.
		if err := s.waitingCommands(ctx, w, pr.Number); err != nil {
			return err
		}
		if err := s.status(ctx, w, pr.Number, pr.HeadSHA, w.waitingStatus()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) waitingCommands(ctx context.Context, w *worker, number int) error {
	key := fmt.Sprintf("waiting-cursor/%d", number)
	var since time.Time
	for _, cursor := range []string{key, fmt.Sprintf("cursor/%d", number)} {
		raw, err := s.store.Observation(ctx, w.id, cursor)
		if err != nil {
			return err
		}
		if len(raw) > 0 {
			var at time.Time
			if err := json.Unmarshal(raw, &at); err != nil {
				return err
			}
			since = maxTime(since, at)
		}
	}
	comments, err := w.Driver.ListComments(ctx, w.repo(), number, since)
	if err != nil {
		return err
	}
	for _, c := range comments {
		if fields := strings.Fields(command(c.Body)); len(fields) > 0 && fields[0] == "queue" && c.Author != w.BotLogin {
			receipt, err := s.store.Observation(ctx, w.id, fmt.Sprintf("comment/%d", c.ID))
			if err != nil {
				return err
			}
			if len(receipt) == 0 {
				if err := s.ignoreWaitingCommand(ctx, w, number, c); err != nil {
					return err
				}
			}
		}
		since = maxTime(since, c.CreatedAt)
	}
	return s.record(ctx, w, key, since)
}

func (s *Server) ignoreWaitingCommand(ctx context.Context, w *worker, number int, comment forge.Comment) error {
	seen, err := s.store.Observation(ctx, w.id, fmt.Sprintf("ignored/comment/%d", comment.ID))
	if err != nil || len(seen) > 0 {
		return err
	}
	allowed, err := w.Driver.CanWrite(ctx, w.repo(), comment.Author)
	if err != nil {
		return err
	}
	if !allowed {
		delete(w.summaries, number)
		return s.record(ctx, w, fmt.Sprintf("ignored/comment/%d", comment.ID), true)
	}
	return s.logIgnoredCommand(ctx, w, number, comment.ID)
}

func (s *Server) logIgnoredCommand(ctx context.Context, w *worker, number int, commentID int64) error {
	// Force reconciliation to read pending commands when readiness recovers,
	// even if the forge's PR summary and configuration have not changed.
	delete(w.summaries, number)
	key := fmt.Sprintf("ignored/comment/%d", commentID)
	seen, err := s.store.Observation(ctx, w.id, key)
	if err != nil || len(seen) > 0 {
		return err
	}
	if err := s.record(ctx, w, key, true); err != nil {
		return err
	}
	s.options.Logger.Info("ignored @openmerge queue command", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "pr", number, "comment", commentID, "reason", w.readinessReason())
	return nil
}
