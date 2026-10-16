package queue

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Apply folds one event without mutating s or any slices or maps reachable from it.
func Apply(s State, e Event) State {
	s = cloneState(s)
	s.revision++
	switch e := e.(type) {
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
		if s.entry(e.PR) == nil {
			observation, known := s.observations[e.PR]
			switch {
			case !known || !observation.Eligible:
				s.report(e.PR, string(NotEligible), "PR does not satisfy queue entry conditions")
			case !observation.Trusted:
				s.report(e.PR, string(Untrusted), "PR head has not passed the trust gate")
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
			s.discardBatch()
		}
	case ResumeRequested:
		s.buildRetry = buildRetryState{}
	case TestRefBuilt:
		if s.batchIn(e.BatchID, Building) {
			if earlier, used := s.testedBatches[e.TestedSHA]; used {
				s.failBuild(e.At, fmt.Sprintf("tested SHA %s was already recorded for batch %s; the top commit must carry the batch ID", e.TestedSHA, earlier))
				break
			}
			s.recordTestedSHA(e.TestedSHA, e.BatchID)
			s.Active.TestedSHA = e.TestedSHA
			s.Active.Method = e.Method
			s.Active.StartedTestingAt = e.At
			s.Active.Phase = Testing
			s.buildRetry = buildRetryState{}
			if check, retained := s.checks[e.TestedSHA]; retained {
				delete(s.checks, e.TestedSHA)
				s.completeChecks(check.success)
			}
		}
	case TestRefBuildFailed:
		if s.batchIn(e.BatchID, Building) {
			if e.Conflict {
				for _, pr := range s.Active.PRs {
					s.requestDequeue(pr.PR, Conflict)
				}
			} else {
				s.failBuild(e.At, "")
			}
		}
	case ChecksCompleted:
		if s.Active != nil && s.Active.Phase == Testing && s.Active.TestedSHA == e.TestedSHA {
			s.completeChecks(e.Success)
		} else if s.Active != nil {
			if _, used := s.testedBatches[e.TestedSHA]; !used {
				s.retainChecks(e)
			}
		}
	case ChecksRefreshed:
		if s.batchIn(e.BatchID, Testing) && s.Active.TestedSHA == e.TestedSHA {
			if s.Active.checkRefreshes == 0 || e.At.After(s.Active.checksRefreshedAt) {
				s.Active.checksRefreshedAt = e.At
			}
			s.Active.checkRefreshes++
		}
	case LandingStarted:
		if s.batchIn(e.BatchID, Testing) {
			s.Active.Phase = Landing
			s.Active.LandingAttempt = LandingAttempt{ID: e.AttemptID, FenceToken: e.FenceToken, StartedAt: e.At}
			for _, pr := range s.Active.PRs {
				if _, pending := s.dequeues[pr.PR]; pending {
					s.entry(pr.PR).DequeueRequested = true
				}
			}
		}
	case Landed:
		if s.batchIn(e.BatchID, Landing) {
			for _, pr := range s.Active.PRs {
				detail := "PR landed"
				if entry := s.entry(pr.PR); entry != nil && entry.DequeueRequested {
					detail = "already landed"
				}
				s.removeEntry(pr.PR)
				s.report(pr.PR, "merged", detail)
			}
			s.BaseSHA, s.Fence = e.NewBaseSHA, e.NewFence
			s.discardBatch()
		}
	case LandingRejected:
		if s.batchIn(e.BatchID, Landing) {
			s.BaseSHA, s.Fence = e.ActualBaseSHA, e.ActualFence
			s.discardBatch()
		}
	case LandingAbandoned:
		if s.batchIn(e.BatchID, Landing) {
			s.Fence = e.NewFence
			s.discardBatch()
		}
	case Dequeued:
		if entry := s.entry(e.PR); entry != nil && entry.AdmissionID == e.AdmissionID && !s.isLanding(e.PR) &&
			(e.BatchID == "" || s.Active != nil && s.Active.ID == e.BatchID) {
			if s.inBatch(e.PR) {
				s.discardBatch()
			}
			s.removeEntry(e.PR)
			s.report(e.PR, "dequeued", string(e.Reason))
		}
	case ActionCompleted:
		s.completeEffect(e.Key)
	}
	s.startBatch()
	s.pruneAcknowledgements()
	return s
}

func cloneState(s State) State {
	s.Entries = slices.Clone(s.Entries)
	if s.Active != nil {
		batch := *s.Active
		batch.PRs = slices.Clone(batch.PRs)
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
		s.dequeues[pr] = Dequeue{PR: pr, AdmissionID: entry.AdmissionID, Reason: reason, scope: fmt.Sprintf("event/%d", s.revision)}
	}
	if s.inBatch(pr) && !s.isLanding(pr) {
		s.addEffect(DeleteRef{Ref: s.Active.TestRef})
	}
}

func (s *State) removeEntry(pr int) {
	s.Entries = slices.DeleteFunc(s.Entries, func(e Entry) bool { return e.PR == pr })
	delete(s.dequeues, pr)
}

func (s *State) discardBatch() {
	s.addEffect(DeleteRef{Ref: s.Active.TestRef})
	s.Active = nil
	clear(s.checks)
}

func (s *State) failBuild(at time.Time, detail string) {
	s.buildRetry.failures++
	s.buildRetry.failedAt = at
	s.buildRetry.revision = s.revision
	s.buildRetry.detail = detail
	s.discardBatch()
}

// maxRetainedChecks bounds unrelated or reordered CI observations per active batch.
// Eviction drops the least recently observed SHA, using the event revision.
// Missing outcomes are reconciled by RefreshChecks; retention only saves a read.
const maxRetainedChecks = 64

type retainedCheck struct {
	success  bool
	revision uint64
}

func (s *State) retainChecks(e ChecksCompleted) {
	s.checks[e.TestedSHA] = retainedCheck{success: e.Success, revision: s.revision}
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
	if success {
		s.Active.checksPassed = true
	} else {
		for _, pr := range s.Active.PRs {
			s.requestDequeue(pr.PR, ChecksFailed)
		}
	}
}

func (s *State) startBatch() {
	if s.Active != nil || len(s.Entries) == 0 {
		return
	}
	entry := s.Entries[0]
	if _, pending := s.dequeues[entry.PR]; pending {
		return
	}
	s.nextBatch++
	id := fmt.Sprintf("b%d", s.nextBatch)
	s.Active = &Batch{
		ID: id, PRs: []BatchPR{{PR: entry.PR, HeadSHA: entry.HeadSHA}},
		BaseSHA: s.BaseSHA, TestRef: "openmerge/queue/" + s.BaseBranch + "/" + id, Phase: Building,
	}
}

type effectScope struct {
	pr        int
	admission uint64
	batch     string
}

func (s *State) report(pr int, status, detail string) {
	report := ReportStatus{PR: pr, Status: status, Detail: detail, revision: s.revision}
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
	if len(s.Entries) > 0 && s.buildRetry.failures > 0 && s.buildFailureReport().Key() == key {
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
