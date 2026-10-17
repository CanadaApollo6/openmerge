package queue

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Apply folds one event without mutating s or any slices or maps reachable from it.
func Apply(s State, e Event) State {
	if _, poisoned := e.(ActionPoisoned); poisoned && s.actionPoison.key != "" {
		return s
	}
	s = cloneState(s)
	s.revision++
	switch e := e.(type) {
	case LifecycleChanged:
		if e.Validate() == nil {
			s.Lifecycle, s.lifecycleRevision = e, s.revision
		}
	case ConfigApplied:
		if e.Validate() != nil || e.Digest == s.ConfigDigest {
			break
		}
		s.ConfigCommit, s.ConfigDigest = e.Commit, e.Digest
		s.buildRetry = buildRetryState{}
		// Entries retain admission order and age; old retry/bisection groups
		// must not bypass the new policy's batch limits or formation delay.
		s.pending = nil
		if s.Active != nil && s.Active.Phase != Landing {
			s.discardBatch()
		}
	case FenceInitialized:
		if s.Fence == "" && e.Validate() == nil {
			s.Fence = e.Token
		}
	case PRMetadataObserved:
		// Fold the invalidation here, where the active phase is serialized with
		// the observation. A pre-append phase read could race TestRefBuilt.
		if s.Active != nil && slices.ContainsFunc(s.Active.PRs, func(pin BatchPR) bool { return pin.PR == e.PR }) {
			s.refreshChecks()
		}
	case PRObserved:
		s.observations[e.PR] = e
		if entry := s.entry(e.PR); entry != nil {
			switch {
			case entry.HeadSHA != e.HeadSHA:
				s.requestDequeue(e.PR, HeadChanged)
			case !e.Eligible:
				s.requestDequeue(e.PR, NotEligible)
			case !e.Trusted:
				s.requestDequeue(e.PR, Untrusted)
			}
		}
	case EnqueueRequested:
		if s.lifecycleBlocked() {
			break
		}
		if s.entry(e.PR) == nil {
			observation, known := s.observations[e.PR]
			switch {
			case !known || !observation.Eligible:
				s.report(e.PR, "blocked", string(NotEligible), "PR does not satisfy queue entry conditions")
			case !observation.Trusted:
				s.report(e.PR, "blocked", string(Untrusted), "PR head has not passed the trust gate")
			default:
				s.nextAdmission++
				s.Entries = append(s.Entries, Entry{PR: e.PR, AdmissionID: s.nextAdmission, HeadSHA: observation.HeadSHA, EnqueuedAt: e.At})
			}
		}
	case DequeueRequested:
		s.requestDequeue(e.PR, Requested)
	case PRClosed:
		delete(s.observations, e.PR)
		s.requestDequeue(e.PR, Closed)
	case BaseMoved:
		if e.SHA == s.BaseSHA {
			break
		}
		s.BaseSHA = e.SHA
		s.buildRetry = buildRetryState{}
		if s.Active != nil && s.Active.Phase != Landing && s.Active.BaseSHA != e.SHA {
			s.rebuildBatch()
		}
	case RefreshRequested:
		s.refreshChecks()
	case PauseRequested:
		s.blockRevisions[Operator]++
		s.pause, s.pauseRevision = e, s.revision
	case RulesBlocked:
		s.blockRulesSource(Rulesets, e.Problems)
	case RulesCleared:
		s.clearRulesSource(Rulesets, e.Basis)
	case RulesSourceBlocked:
		if e.Validate() == nil {
			s.blockRulesSource(e.Source, e.Problems)
		}
	case RulesSourceCleared:
		if e.Validate() == nil {
			s.clearRulesSource(e.Source, e.Basis)
		}
	case BlockSourceBlocked:
		if e.Validate() == nil {
			alreadyPoisoned := e.Source == ActionPoison && s.SourceBlocked(ActionPoison)
			s.clearSourceState(e.Source)
			s.blockSources[e.Source] = e
			s.blockRevisions[e.Source]++
			// A poisoned pause report must not mint another report key. Its
			// source revision still advances to invalidate prepared clears.
			if !alreadyPoisoned {
				s.blockPauseRevision = s.revision
			}
		}
	case BlockSourceCleared:
		if e.Validate() == nil && e.Basis == s.BlockSourceRevision(e.Source) {
			s.clearSourceState(e.Source)
			delete(s.blockSources, e.Source)
			s.blockRevisions[e.Source]++
			s.blockPauseRevision = s.revision
		}
	case ResumeRequested:
		s.blockRevisions[Operator]++
		s.blockRevisions[ActionPoison]++
		s.pause = PauseRequested{}
		s.buildRetry = buildRetryState{}
		s.actionPoison = actionPoisonState{}
	case ActionPoisoned:
		s.blockRevisions[ActionPoison]++
		s.actionPoison = actionPoisonState{key: e.Key, reason: e.Reason, revision: s.revision}
	case BatchStarted:
		if e.Validate() == nil && s.Active == nil && e.BatchID == s.nextBatchID() && e.BaseSHA == s.BaseSHA && s.runnablePRs(e.PRs) {
			fromBisection := false
			if len(s.pending) > 0 {
				group := s.pending[0]
				if !slices.Equal(group.prs, e.PRs) {
					break
				}
				fromBisection = group.bisection
				s.pending = s.pending[1:]
			}
			s.nextBatch++
			s.Active = &Batch{ID: e.BatchID, BaseSHA: e.BaseSHA, ConfigDigest: s.ConfigDigest, PRs: slices.Clone(e.PRs), FromBisection: fromBisection,
				TestRef: "openmerge/queue/" + s.BaseBranch + "/" + e.BatchID, Phase: Building}
		}
	case BatchFailed:
		if e.Reason == ChecksTimeout && s.batchIn(e.BatchID, Testing) && !s.Active.checksPassed {
			s.failBatch(e.Reason)
		}
	case FastForwardBatchSelected:
		s.selectFastForward(e)
	case FastForwardBlocked:
		if s.batchIn(e.BatchID, Building) && s.Active.Method == FastForward {
			s.requestDequeue(s.Active.PRs[0].PR, FastForwardRequired)
			s.rebuildBatch()
		}
	case FastForwardMergeCommitsBlocked:
		if e.Validate() == nil && s.batchIn(e.BatchID, Building) && s.Active.Method == FastForward && slices.ContainsFunc(s.Active.PRs, func(pr BatchPR) bool { return pr.PR == e.PR }) {
			s.requestDequeue(e.PR, MergeCommitsNotAllowed)
			s.rebuildBatch()
		}
	case FastForwardRefBuilt:
		if e.Validate() == nil && s.batchIn(e.BatchID, Building) && s.Active.Method == FastForward && e.HeadSHA == s.Active.PRs[len(s.Active.PRs)-1].HeadSHA && e.TestedSHA != e.HeadSHA {
			s.applyBuilt(TestRefBuilt{BatchID: e.BatchID, TestedSHA: e.TestedSHA, At: e.At, Method: FastForward})
		}
	case TestRefBuilt:
		s.applyBuilt(e)
	case TestRefBuildFailed:
		if s.batchIn(e.BatchID, Building) {
			if e.Conflict {
				s.failBatch(Conflict)
			} else {
				s.failBuild(e.At, "")
			}
		}
	case ChecksObserved:
		s.observeChecks(e)
	case CheckRerunRequested:
		s.requestCheckRerun(e)
	case CheckRerunDispatchProgress:
		s.progressCheckRerun(e)
	case CheckRerunDispatched:
		s.dispatchCheckRerun(e)
	case ChecksFailureConfirmed:
		s.confirmCheckFailure(e)
	case ChecksCompleted:
		if s.Active != nil && s.Active.Phase == Testing && s.Active.TestedSHA == e.TestedSHA {
			s.completeChecks(e.Success)
		} else if s.Active != nil {
			if _, used := s.testedBatches[e.TestedSHA]; !used {
				s.retainChecks(e.TestedSHA, retainedCheck{terminal: true, success: e.Success})
			}
		}
	case ChecksReopened:
		if s.Active != nil && s.Active.Phase == Testing && s.Active.TestedSHA == e.TestedSHA {
			s.Active.checksGeneration++
			s.Active.checksPassed = false
			s.Active.needsRefresh = true
		} else if s.Active != nil {
			if _, used := s.testedBatches[e.TestedSHA]; !used {
				s.retainChecks(e.TestedSHA, retainedCheck{})
			}
		}
	case CheckProvenanceObserved:
		if e.Validate() == nil && s.batchIn(e.BatchID, Testing) && s.Active.TestedSHA == e.TestedSHA {
			s.Active.unboundChecks = slices.Clone(e.Checks)
		}
	case ChecksRefreshed:
		if s.batchIn(e.BatchID, Testing) && s.Active.TestedSHA == e.TestedSHA {
			s.Active.checksRefreshedAt = e.At
			s.Active.checkRefreshes++
			s.Active.needsRefresh = false
		}
	case LandingStarted:
		if s.batchIn(e.BatchID, Testing) {
			s.Active.Phase = Landing
			s.Active.LandingAttempt = LandingAttempt{ID: e.AttemptID, FenceToken: e.FenceToken, StartedAt: e.At}
			if s.Active.Method == FastForward {
				s.Active.LandingAttempt.FastForwardHeadSHA = s.Active.PRs[len(s.Active.PRs)-1].HeadSHA
			}
			for _, pr := range s.Active.PRs {
				if _, pending := s.dequeues[pr.PR]; pending {
					s.entry(pr.PR).DequeueRequested = true
				}
			}
		}
	case LandingCleanupPolicyRecorded:
		if e.Validate() == nil && s.batchIn(e.BatchID, Landing) && s.Active.LandingAttempt.ID == e.AttemptID && !s.Active.LandingAttempt.cleanupPolicyRecorded {
			s.Active.LandingAttempt.DeleteHeadBranch = e.DeleteHeadBranch
			s.Active.LandingAttempt.cleanupPolicyRecorded = true
		}
	case HeadCleanupRequested:
		if s.batchIn(e.BatchID, Landing) && s.Active.LandingAttempt.DeleteHeadBranch && (s.Active.TestedSHA == e.LandedSHA && s.Active.LandingAttempt.FastForwardHeadSHA == "" || s.Active.LandingAttempt.FastForwardHeadSHA == e.LandedSHA) {
			s.Active.cleanupRequested = true
		}
	case Landed:
		if s.batchIn(e.BatchID, Landing) {
			for _, pr := range s.Active.PRs {
				detail := "PR landed"
				if entry := s.entry(pr.PR); entry != nil && entry.DequeueRequested {
					detail = "already landed"
				}
				s.reportHead(pr.PR, pr.HeadSHA, "merged", "", detail)
				if s.Active.Method == Squash || s.Active.Method == Rebase {
					s.addEffect(MarkLanded{PR: pr.PR, HeadSHA: pr.HeadSHA, LandedSHA: e.NewBaseSHA})
				}
				if s.Active.cleanupRequested {
					s.addEffect(DeleteHeadBranch{PR: pr.PR, HeadSHA: pr.HeadSHA, LandedSHA: e.NewBaseSHA})
				}
				s.removeEntry(pr.PR)
			}
			s.BaseSHA, s.Fence = e.NewBaseSHA, e.NewFence
			s.discardBatch()
		}
	case LandingAbandoned:
		if s.batchIn(e.BatchID, Landing) {
			s.Fence = e.NewFence
			if s.Active.ConfigDigest != s.ConfigDigest {
				s.discardBatch()
			} else {
				s.rebuildBatch()
			}
		}
	case Dequeued:
		if entry := s.entry(e.PR); entry != nil && entry.AdmissionID == e.AdmissionID && !s.isLanding(e.PR) &&
			(e.BatchID == "" || s.Active != nil && s.Active.ID == e.BatchID) {
			detail := s.DequeueDetail(e.PR, e.Reason)
			if s.inBatch(e.PR) {
				s.rebuildBatch()
			}
			s.reportHead(e.PR, entry.HeadSHA, "dequeued", string(e.Reason), detail)
			s.removeEntry(e.PR)
		}
	case ActionCompleted:
		s.completeEffect(e.Key)
	}
	s.pruneAcknowledgements()
	return s
}

func (s *State) refreshChecks() {
	if s.Active != nil && s.Active.Phase == Testing {
		s.Active.checksPassed, s.Active.needsRefresh = false, true
		s.Active.checksGeneration++
	}
}

func cloneState(s State) State {
	s.blockSources = maps.Clone(s.blockSources)
	if s.blockSources == nil {
		s.blockSources = make(map[BlockSource]BlockSourceBlocked)
	}
	s.blockRevisions = maps.Clone(s.blockRevisions)
	if s.blockRevisions == nil {
		s.blockRevisions = make(map[BlockSource]uint64)
	}

	s.rulesSources = maps.Clone(s.rulesSources)
	if s.rulesSources == nil {
		s.rulesSources = make(map[RulesSource]rulesSourceState)
	}
	for source, current := range s.rulesSources {
		current.problems = slices.Clone(current.problems)
		s.rulesSources[source] = current
	}
	s.Entries = slices.Clone(s.Entries)
	s.pending = slices.Clone(s.pending)
	for i := range s.pending {
		s.pending[i].prs = slices.Clone(s.pending[i].prs)
	}
	if s.Active != nil {
		batch := *s.Active
		batch.PRs = slices.Clone(batch.PRs)
		batch.unboundChecks = slices.Clone(batch.unboundChecks)
		if batch.CheckRetry != nil {
			retry := *batch.CheckRetry
			retry.Dispatches = slices.Clone(retry.Dispatches)
			retry.FailedRuns = slices.Clone(retry.FailedRuns)
			retry.ObservedRuns = slices.Clone(retry.ObservedRuns)
			batch.CheckRetry = &retry
		}
		s.Active = &batch
	}
	s.observations = maps.Clone(s.observations)
	if s.observations == nil {
		s.observations = make(map[int]PRObserved)
	}
	s.dequeues = maps.Clone(s.dequeues)
	if s.dequeues == nil {
		s.dequeues = make(map[int]Dequeue)
	}
	s.effects = slices.Clone(s.effects)
	s.checks = maps.Clone(s.checks)
	if s.checks == nil {
		s.checks = make(map[string]retainedCheck)
	}
	s.testedBatches = maps.Clone(s.testedBatches)
	s.testedOrder = slices.Clone(s.testedOrder)
	if s.testedBatches == nil {
		s.testedBatches = make(map[string]string)
	}
	s.acknowledged = maps.Clone(s.acknowledged)
	if s.acknowledged == nil {
		s.acknowledged = make(map[string]effectScope)
	}
	return s
}

func (s *State) entry(pr int) *Entry {
	for i := range s.Entries {
		if s.Entries[i].PR == pr {
			return &s.Entries[i]
		}
	}
	return nil
}

func (s State) inBatch(pr int) bool {
	if s.Active != nil {
		for _, member := range s.Active.PRs {
			if member.PR == pr {
				return true
			}
		}
	}
	return false
}

func (s State) isLanding(pr int) bool {
	return s.Active != nil && s.Active.Phase == Landing && s.inBatch(pr)
}

func (s State) batchIn(id string, phase Phase) bool {
	return s.Active != nil && s.Active.ID == id && s.Active.Phase == phase
}

func (s *State) requestDequeue(pr int, reason DequeueReason) {
	entry := s.entry(pr)
	if entry == nil {
		return
	}
	if s.isLanding(pr) {
		entry.DequeueRequested = true
	}
	if current, pending := s.dequeues[pr]; !pending || reason == Requested && current.Reason != Requested {
		s.dequeues[pr] = Dequeue{PR: pr, HeadSHA: entry.HeadSHA, AdmissionID: entry.AdmissionID, Reason: reason, scope: fmt.Sprintf("event/%d", s.revision)}
	}
	s.removePendingPR(pr)
	if s.inBatch(pr) && !s.isLanding(pr) {
		s.addEffect(DeleteRef{Ref: s.Active.TestRef})
	}
}

func (s *State) removeEntry(pr int) {
	s.removePendingPR(pr)
	s.Entries = slices.DeleteFunc(s.Entries, func(e Entry) bool { return e.PR == pr })
	delete(s.dequeues, pr)
}

func (s *State) discardBatch() {
	s.addEffect(DeleteRef{Ref: s.Active.TestRef})
	s.Active = nil
	clear(s.checks)
}

func (s *State) failBuild(at time.Time, detail string) {
	s.blockRevisions[BuildFailed]++
	s.buildRetry.failures++
	s.buildRetry.failedAt = at
	s.buildRetry.revision = s.revision
	s.buildRetry.detail = detail
	s.rebuildBatch()
}

// maxRetainedChecks bounds unrelated or reordered CI observations per active batch.
// Eviction drops the least recently observed SHA, using the event revision.
// Missing outcomes are reconciled by RefreshChecks; retention only saves a read.
const maxRetainedChecks = 64

type retainedCheck struct {
	terminal, success bool
	revision          uint64
}

func (s *State) retainChecks(sha string, check retainedCheck) {
	check.revision = s.revision
	s.checks[sha] = check
	if len(s.checks) > maxRetainedChecks {
		var oldest string
		revision := ^uint64(0)
		for sha, check := range s.checks {
			if check.revision < revision {
				oldest, revision = sha, check.revision
			}
		}
		delete(s.checks, oldest)
	}
}

func (s *State) completeChecks(success bool) {
	if s.Active.CheckRetry != nil && s.Active.CheckRetry.Confirmed {
		return
	}
	if success {
		s.Active.checksPassed = true
	} else {
		s.failBatch(ChecksFailed)
	}
}

func (s State) nextBatchID() string { return fmt.Sprintf("b%d", s.nextBatch+1) }

type pendingGroup struct {
	prs       []BatchPR
	bisection bool
}

func (s State) runnablePRs(prs []BatchPR) bool {
	for _, pr := range prs {
		entry := s.entry(pr.PR)
		observation, known := s.observations[pr.PR]
		if entry == nil || entry.HeadSHA != pr.HeadSHA || !known || !observation.Eligible || !observation.Trusted || observation.HeadSHA != pr.HeadSHA {
			return false
		}
		if _, pending := s.dequeues[pr.PR]; pending {
			return false
		}
	}
	return len(prs) > 0
}

func (s *State) removePendingPR(pr int) {
	for i := range s.pending {
		s.pending[i].prs = slices.DeleteFunc(s.pending[i].prs, func(p BatchPR) bool { return p.PR == pr })
	}
	s.pending = slices.DeleteFunc(s.pending, func(g pendingGroup) bool { return len(g.prs) == 0 })
}

func (s *State) rebuildBatch() {
	group := pendingGroup{prs: slices.Clone(s.Active.PRs), bisection: s.Active.FromBisection}
	group.prs = slices.DeleteFunc(group.prs, func(pr BatchPR) bool { _, pending := s.dequeues[pr.PR]; return pending })
	if len(group.prs) > 0 {
		s.pending = append([]pendingGroup{group}, s.pending...)
	}
	s.discardBatch()
}

func (s *State) failBatch(reason DequeueReason) {
	prs := s.Active.PRs
	if len(prs) == 1 {
		s.requestDequeue(prs[0].PR, reason)
		return
	}
	middle := len(prs) / 2
	groups := []pendingGroup{
		{prs: slices.Clone(prs[:middle]), bisection: true},
		{prs: slices.Clone(prs[middle:]), bisection: true},
	}
	s.pending = append(groups, s.pending...)
	for _, pr := range prs {
		if !s.runnablePRs([]BatchPR{pr}) {
			s.removePendingPR(pr.PR)
		}
	}
	s.discardBatch()
}

type effectScope struct {
	pr        int
	admission uint64
	batch     string
}

func (s *State) report(pr int, status, reason, detail string) {
	head := s.observations[pr].HeadSHA
	if entry := s.entry(pr); entry != nil {
		head = entry.HeadSHA
	}
	s.reportHead(pr, head, status, reason, detail)
}

func (s *State) reportHead(pr int, head, status, reason, detail string) {
	report := ReportStatus{PR: pr, HeadSHA: head, Status: status, Reason: reason, Detail: detail, revision: s.revision}
	if entry := s.entry(pr); entry != nil {
		report.admission = entry.AdmissionID
	}
	s.addEffect(report)
}

func (s *State) addEffect(action Action) {
	if _, done := s.acknowledged[action.Key()]; done {
		return
	}
	for _, pending := range s.effects {
		if pending.Key() == action.Key() {
			return
		}
	}
	s.effects = append(s.effects, action)
}

func (s *State) completeEffect(key string) {
	if len(s.Entries) > 0 && (s.buildRetry.failures > 0 || s.workBlocked()) && s.pauseReport().Key() == key {
		s.acknowledged[key] = effectScope{pr: s.Entries[0].PR, admission: s.Entries[0].AdmissionID}
	}
	for _, effect := range s.effects {
		if effect.Key() == key {
			if effect, ok := effect.(ReportStatus); ok {
				s.acknowledged[key] = effectScope{pr: effect.PR, admission: effect.admission}
			}
		}
	}
	if s.Active != nil && (DeleteRef{Ref: s.Active.TestRef}).Key() == key {
		s.acknowledged[key] = effectScope{batch: s.Active.ID}
	}
	s.effects = slices.DeleteFunc(s.effects, func(a Action) bool { return a.Key() == key })
}

func (s *State) pruneAcknowledgements() {
	for key, scope := range s.acknowledged {
		if scope.pr != 0 {
			if entry := s.entry(scope.pr); entry != nil && entry.AdmissionID == scope.admission {
				continue
			}
		}
		if scope.batch != "" && s.Active != nil && s.Active.ID == scope.batch {
			continue
		}
		delete(s.acknowledged, key)
	}
}

// testedHistory bounds the reuse guard. Uniqueness comes from the batch ID in
// each test commit; the guard only needs to catch an executor that violates it
// with a recent SHA, so older entries are evicted rather than kept forever.
const testedHistory = 64

func (s *State) recordTestedSHA(sha, batchID string) {
	s.testedBatches[sha] = batchID
	s.testedOrder = append(s.testedOrder, sha)
	for len(s.testedOrder) > testedHistory {
		delete(s.testedBatches, s.testedOrder[0])
		s.testedOrder = s.testedOrder[1:]
	}
}

func (s *State) applyBuilt(e TestRefBuilt) {
	if s.batchIn(e.BatchID, Building) {
		if earlier, used := s.testedBatches[e.TestedSHA]; used {
			s.failBuild(e.At, fmt.Sprintf("tested SHA %s was already recorded for batch %s; the top commit must carry the batch ID", e.TestedSHA, earlier))
			return
		}
		s.recordTestedSHA(e.TestedSHA, e.BatchID)
		s.Active.TestedSHA = e.TestedSHA
		s.Active.Method = e.Method
		s.Active.StartedTestingAt = e.At
		s.Active.Phase = Testing
		s.buildRetry = buildRetryState{}
		if check, retained := s.checks[e.TestedSHA]; retained {
			delete(s.checks, e.TestedSHA)
			s.Active.needsRefresh = !check.terminal
			if check.terminal {
				s.completeChecks(check.success)
			}
		}
	}
}
