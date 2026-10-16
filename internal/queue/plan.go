package queue

import (
	"fmt"
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

func plan(cfg Config, s State, now time.Time) []Action {
	var actions []Action
	if s.actionPoison.key != "" {
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
			if deletion, ok := effect.(DeleteRef); ok && s.Active != nil && s.Active.Phase == Landing && deletion.Ref == s.Active.TestRef {
				continue
			}
			actions = append(actions, effect)
		}
	}
	batch := s.Active
	if batch == nil {
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
	if s.actionPoison.key != "" {
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
			})
			return actions
		}
		deadline := batch.StartedTestingAt.Add(cfg.CheckTimeout)
		timedOut := cfg.CheckTimeout > 0 && now.After(deadline)
		if timedOut && batch.checkRefreshes > 0 && !batch.checksRefreshedAt.Before(deadline) {
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
			if timedOut || batch.checkRefreshes == 0 || now.Sub(batch.checksRefreshedAt) >= interval {
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

// PauseReason reports a poison pause or the configured build failure limit.
// ResumeRequested clears either pause. A changed base or successful build clears
// only the build failure streak. Apply has no config, so Plan evaluates its limit.
func (s State) PauseReason(cfg Config) string {
	if s.actionPoison.key != "" {
		return "action-poisoned"
	}
	limit := cfg.BuildRetries
	if limit <= 0 {
		limit = 3
	}
	if s.buildRetry.failures >= limit {
		return "build-failed"
	}
	return ""
}

func (s State) pauseReport() ReportStatus {
	if s.actionPoison.key != "" {
		return ReportStatus{
			PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: "action-poisoned",
			revision: s.actionPoison.revision, admission: s.Entries[0].AdmissionID,
			Detail: fmt.Sprintf("Queue paused after repeated invalid action %s: %s; resume the queue to retry", s.actionPoison.key, s.actionPoison.reason),
		}
	}
	detail := fmt.Sprintf("Queue paused after %d consecutive build failures; resume the queue or update the base branch", s.buildRetry.failures)
	if s.buildRetry.detail != "" {
		detail = fmt.Sprintf("Test ref build failed (%d consecutive failures): %s", s.buildRetry.failures, s.buildRetry.detail)
	}
	return ReportStatus{
		PR: s.Entries[0].PR, HeadSHA: s.Entries[0].HeadSHA, Status: "paused", Reason: "build-failed", revision: s.buildRetry.revision, admission: s.Entries[0].AdmissionID,
		Detail: detail,
	}
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
