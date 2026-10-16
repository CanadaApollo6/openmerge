package queue

import "time"

// NextWake returns the next future time at which a planner timer can change the
// plan. Callers must first drain ready actions (and schedule failed-action
// retries themselves). A paused build has no timer: it needs an external event.
func NextWake(cfg Config, s State, now time.Time) (time.Time, bool) {
	batch := s.Active
	if batch == nil {
		return time.Time{}, false
	}
	var next time.Time
	consider := func(at time.Time) {
		if at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	if batch.Phase == Landing {
		// Plan uses a strict comparison for resolution and check timeouts.
		consider(batch.LandingAttempt.StartedAt.Add(cfg.ResolveAfter).Add(time.Nanosecond))
	} else {
		for _, pr := range batch.PRs {
			if _, pending := s.dequeues[pr.PR]; pending {
				return time.Time{}, false
			}
		}
		switch batch.Phase {
		case Building:
			if s.buildRetry.failures > 0 && s.PauseReason(cfg) == "" {
				consider(s.buildRetry.failedAt.Add(cfg.buildRetryDelay(s.buildRetry.failures)))
			}
		case Testing:
			if !batch.checksPassed {
				if cfg.CheckTimeout > 0 {
					consider(batch.StartedTestingAt.Add(cfg.CheckTimeout).Add(time.Nanosecond))
				}
				if batch.checkRefreshes > 0 {
					interval := cfg.RefreshInterval
					if interval <= 0 {
						interval = time.Minute
					}
					consider(batch.checksRefreshedAt.Add(interval))
				}
			}
		}
	}
	return next, !next.IsZero()
}
