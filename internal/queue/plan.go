package queue

import (
	"fmt"
	"reflect"
	"slices"
	"time"
)

// Plan returns intended actions in queue order, then recorded side effects, then
// the active batch's next action. Replanning unchanged inputs preserves all keys.
// Invalid inputs that would produce an unsafe action panic before it is emitted.
func Plan(cfg Config, s State, now time.Time) []Action {
	actions := plan(cfg, s, now)
	for _, action := range actions {
		if err := action.Validate(); err != nil {
			panic(fmt.Sprintf("queue planner emitted invalid %T: %v", action, err))
		}
	}
	return actions
}

// DeferredByPause distinguishes temporarily suppressed work from actions that
// the resumed plan has superseded. Resume is projected without appending it.
func DeferredByPause(cfg Config, s State, action Action, now time.Time) bool {
	if s.PauseReason(cfg) == "" {
		return false
	}
	unblocked := Apply(s, ResumeRequested{})
	for _, source := range s.RulesSources() {
		unblocked = Apply(unblocked, RulesSourceCleared{Source: source.Source, Basis: source.Revision})
	}
	return slices.ContainsFunc(Plan(cfg, unblocked, now), func(planned Action) bool {
		return planned.Key() == action.Key()
	})
}

func plan(cfg Config, s State, now time.Time) []Action {
	var actions []Action
	if s.workBlocked() {
		if len(s.Entries) > 0 {
			report := s.pauseReport()
			if _, done := s.acknowledged[report.Key()]; !done {
				actions = append(actions, report)
			}
		}
	} else {
		for _, entry := range s.Entries {
			if action, pending := s.dequeues[entry.PR]; pending && !s.isLanding(entry.PR) {
				actions = append(actions, action)
			}
		}
		for _, effect := range s.effects {
			if cleanup, ok := effect.(DeleteHeadBranch); ok && slices.ContainsFunc(s.effects, func(pending Action) bool {
				mark, ok := pending.(MarkLanded)
				return ok && mark == MarkLanded(cleanup)
			}) {
				continue
			}
			if deletion, ok := effect.(DeleteRef); ok && s.Active != nil && s.Active.Phase == Landing && deletion.Ref == s.Active.TestRef {
				continue
			}
			actions = append(actions, effect)
		}
	}
	batch := s.Active
	if batch == nil {
		if s.workBlocked() {
			return actions
		}
		if s.PauseReason(cfg) != "" || s.buildRetry.detail != "" {
			if len(s.Entries) > 0 {
				report := s.pauseReport()
				if _, done := s.acknowledged[report.Key()]; !done {
					actions = append(actions, report)
				}
			}
		}
		if s.PauseReason(cfg) == "" && (s.buildRetry.failures == 0 || now.Sub(s.buildRetry.failedAt) >= cfg.buildRetryDelay(s.buildRetry.failures)) {
			if prs, _ := s.batchCandidates(cfg, now); len(prs) > 0 {
				id := s.nextBatchID()
				actions = append(actions, BuildTestRef{BatchID: id, BaseSHA: s.BaseSHA, PRs: prs, Ref: "openmerge/queue/" + s.BaseBranch + "/" + id, Method: cfg.MergeMethod})
			}
		}
		return actions
	}
	if batch.Phase == Landing {
		if now.Sub(batch.LandingAttempt.StartedAt) > cfg.ResolveAfter {
			actions = append(actions, ResolveLanding{
				BatchID: batch.ID, AttemptID: batch.LandingAttempt.ID,
				TestedSHA: batch.TestedSHA, FenceToken: batch.LandingAttempt.FenceToken,
			})
		}
		return actions
	}
	if s.workBlocked() {
		return actions
	}
	for _, pr := range batch.PRs {
		if _, pending := s.dequeues[pr.PR]; pending {
			return actions
		}
		observation, known := s.observations[pr.PR]
		if !known || !observation.Eligible || !observation.Trusted || observation.HeadSHA != pr.HeadSHA {
			return actions
		}
	}
	switch batch.Phase {
	case Building:
		paused := s.PauseReason(cfg) != ""
		if paused || s.buildRetry.detail != "" {
			report := s.pauseReport()
			if _, done := s.acknowledged[report.Key()]; !done {
				actions = append(actions, report)
			}
			if paused {
				return actions
			}
		}
		if s.buildRetry.failures > 0 && now.Sub(s.buildRetry.failedAt) < cfg.buildRetryDelay(s.buildRetry.failures) {
			return actions
		}
		actions = append(actions, BuildTestRef{
			BatchID: batch.ID, BaseSHA: batch.BaseSHA, PRs: slices.Clone(batch.PRs), Ref: batch.TestRef,
			Method: cfg.MergeMethod,
		})
	case Testing:
		if batch.checksPassed {
			actions = append(actions, Land{
				BatchID: batch.ID, ExpectedBaseSHA: batch.BaseSHA, TestedSHA: batch.TestedSHA,
				Ref: batch.TestRef, FenceToken: s.Fence,
				Method: batch.Method, PRs: slices.Clone(batch.PRs),
				ChecksGeneration: batch.checksGeneration,
			})
			return actions
		}
		deadline := batch.StartedTestingAt.Add(cfg.CheckTimeout)
		timedOut := cfg.CheckTimeout > 0 && now.After(deadline)
		if timedOut && !batch.needsRefresh && batch.checkRefreshes > 0 && !batch.checksRefreshedAt.Before(deadline) {
			if len(batch.PRs) > 1 {
				actions = append(actions, FailBatch{BatchID: batch.ID, Reason: ChecksTimeout})
				return actions
			}
			for _, pr := range batch.PRs {
				actions = append(actions, Dequeue{PR: pr.PR, HeadSHA: pr.HeadSHA, AdmissionID: s.entry(pr.PR).AdmissionID, Reason: ChecksTimeout, BatchID: batch.ID, scope: "batch/" + batch.ID})
			}
			deletion := DeleteRef{Ref: batch.TestRef}
			if _, done := s.acknowledged[deletion.Key()]; !done {
				actions = append(actions, deletion)
			}
		} else {
			interval := cfg.RefreshInterval
			if interval <= 0 {
				interval = time.Minute
			}
			if timedOut || batch.needsRefresh || batch.checkRefreshes == 0 || now.Sub(batch.checksRefreshedAt) >= interval {
				actions = append(actions, RefreshChecks{BatchID: batch.ID, TestedSHA: batch.TestedSHA, epoch: batch.checkRefreshes})
			}
		}
	}
	return actions
}

type buildRetryState struct {
	failures int
	failedAt time.Time
	revision uint64
	detail   string
}

type actionPoisonState struct {
	key, reason string
	revision    uint64
}

func (s State) workBlocked() bool {
	return s.pause.Reason != "" || s.actionPoison.key != "" || s.hasRulesProblems()
}

// Block describes one active reason that prevents new work.
type Block struct{ Reason, Detail string }

// Blocks returns all active reasons, with the primary pause first.
func (s State) Blocks(cfg Config) []Block {
	blocks := make([]Block, 0, 4)
	if s.pause.Reason != "" {
		blocks = append(blocks, Block{s.pause.Reason, s.pause.Detail})
	}
	if s.actionPoison.key != "" {
		blocks = append(blocks, Block{"action-poisoned", s.poisonDetail()})
	}
	for _, source := range rulesSourceOrder {
		if len(s.rulesSources[source].problems) > 0 {
			blocks = append(blocks, Block{"repository-rules", s.rulesSourceDetail(source)})
		}
	}
	if s.buildLimitReached(cfg) {
		blocks = append(blocks, Block{"build-failed", s.buildFailureDetail()})
	}
	return blocks
}

// PauseReason reports the primary active block. RulesCleared never clears a
// generic pause, poison or build failure. Apply has no config, so Plan evaluates
// the build failure limit.
func (s State) PauseReason(cfg Config) string {
	if s.pause.Reason != "" {
		return s.pause.Reason
	}
	if s.actionPoison.key != "" {
		return "action-poisoned"
	}
	if s.hasRulesProblems() {
		return "repository-rules"
	}
	if s.buildLimitReached(cfg) {
		return "build-failed"
	}
	return ""
}

func (s State) buildLimitReached(cfg Config) bool {
	limit := cfg.BuildRetries
	if limit <= 0 {
		limit = 3
	}
	return s.buildRetry.failures >= limit
}

func (s State) pauseReport() ReportStatus {
	if s.pause.Reason != "" {
		return ReportStatus{PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: s.pause.Reason, Detail: s.pause.Detail, revision: s.pauseRevision, admission: s.Entries[0].AdmissionID}
	}
	if s.actionPoison.key != "" {
		return ReportStatus{
			PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: "action-poisoned",
			revision: s.actionPoison.revision, admission: s.Entries[0].AdmissionID,
			Detail: s.poisonDetail(),
		}
	}
	if s.hasRulesProblems() {
		return ReportStatus{PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: "repository-rules", Detail: s.rulesDetail(), revision: s.rulesPauseRevision, admission: s.Entries[0].AdmissionID}
	}
	return ReportStatus{
		PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: "build-failed", revision: s.buildRetry.revision, admission: s.Entries[0].AdmissionID,
		Detail: s.buildFailureDetail(),
	}
}

func (s State) poisonDetail() string {
	return fmt.Sprintf("Queue paused after repeated invalid action %s: %s; resume the queue to retry", s.actionPoison.key, s.actionPoison.reason)
}

func (s State) buildFailureDetail() string {
	detail := fmt.Sprintf("Queue paused after %d consecutive build failures; resume the queue or update the base branch", s.buildRetry.failures)
	if s.buildRetry.detail != "" {
		detail = fmt.Sprintf("Test ref build failed (%d consecutive failures): %s", s.buildRetry.failures, s.buildRetry.detail)
	}
	return detail
}

func (cfg Config) buildRetryDelay(failures int) time.Duration {
	delay := cfg.BuildBackoff
	if delay <= 0 {
		return 0
	}
	const maxDelay time.Duration = 1<<63 - 1
	for i := 1; i < failures; i++ {
		if delay > maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	return delay
}

// batchCandidates gives pending sub-batches priority and never waits to retest them.
func (s State) batchCandidates(cfg Config, now time.Time) ([]BatchPR, time.Time) {
	if len(s.pending) > 0 {
		return slices.Clone(s.pending[0].prs), time.Time{}
	}
	size := cfg.BatchMaxSize
	if size == 0 {
		size = 1
	}
	if size < 1 || size > 128 || cfg.BatchMaxWait < 0 {
		panic("invalid batch configuration")
	}
	var prs []BatchPR
	var oldest time.Time
	for _, entry := range s.Entries {
		pr := BatchPR{PR: entry.PR, HeadSHA: entry.HeadSHA}
		if !s.runnablePRs([]BatchPR{pr}) {
			continue
		}
		if len(prs) == 0 || entry.EnqueuedAt.Before(oldest) {
			oldest = entry.EnqueuedAt
		}
		prs = append(prs, pr)
		if len(prs) == size {
			return prs, time.Time{}
		}
	}
	if len(prs) > 0 && size > 1 && cfg.BatchMaxWait > 0 {
		deadline := oldest.Add(cfg.BatchMaxWait)
		if now.Before(deadline) {
			return nil, deadline
		}
	}
	return prs, time.Time{}
}

// CanStart revalidates a durable start against the current plan, including the
// complete action payload. Callers must serialize this check with the append.
func CanStart(cfg Config, s State, action Action, event Event) bool {
	var at time.Time
	switch e := event.(type) {
	case BatchStarted:
		a, ok := action.(BuildTestRef)
		if !ok || e.Validate() != nil || a.BatchID != e.BatchID || a.BaseSHA != e.BaseSHA || !slices.Equal(a.PRs, e.PRs) {
			return false
		}
		at = e.At
	case LandingStarted:
		a, ok := action.(Land)
		if !ok || e.AttemptID == "" || a.BatchID != e.BatchID || a.FenceToken != e.FenceToken {
			return false
		}
		at = e.At
	default:
		return false
	}
	for _, planned := range Plan(cfg, s, at) {
		if planned.Key() == action.Key() && reflect.DeepEqual(planned, action) {
			return true
		}
	}
	return false
}

// PauseDetail gives the human-readable explanation used by queue status checks.
func (s State) PauseDetail(cfg Config) string {
	if blocks := s.Blocks(cfg); len(blocks) > 0 {
		return blocks[0].Detail
	}
	return ""
}
