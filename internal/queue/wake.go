package queue

import "time"

// NextWake returns the next future time at which a planner timer can change the
// plan. Callers must first drain ready actions (and schedule failed-action
// retries themselves). A paused build has no timer: it needs an external event.
func NextWake(cfg Config, s State, now time.Time) (time.Time, bool) {
	next, ok := nextQueueWake(cfg, s, now)
	for _, effect := range s.effects {
		if retry, yes := effect.(RetryCheckCancellation); yes && retry.At.After(now) && (!ok || retry.At.Before(next)) {
			next, ok = retry.At, true
		}
	}
	return next, ok
}

func nextQueueWake(cfg Config, s State, now time.Time) (time.Time, bool) {
	if s.Active == nil || (cfg.MaxDepth() == 1 && len(s.Following) == 0) {
		return nextWakeSingle(cfg, s, now)
	}
	var next time.Time
	consider := func(at time.Time) {
		if at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	for _, b := range s.Batches() {
		if b.InvalidationReason != "" || (b.ProvisionalFailure != "" && b != s.Active) {
			continue
		}
		local := s
		local.Active = b
		if at, ok := nextWakeSingle(cfg, local, now); ok {
			consider(at)
		}
	}
	if s.PauseReason(cfg) == "" && len(s.Batches()) < cfg.MaxDepth() {
		_, at := s.batchCandidates(cfg, now)
		consider(at)
	}
	return next, !next.IsZero()
}

func nextWakeSingle(cfg Config, s State, now time.Time) (time.Time, bool) {
	batch := s.Active
	if batch == nil {
		if s.PauseReason(cfg) != "" {
			return time.Time{}, false
		}
		if s.buildRetry.failures > 0 {
			at := s.buildRetry.failedAt.Add(cfg.buildRetryDelay(s.buildRetry.failures))
			if at.After(now) {
				return at, true
			}
		}
		_, at := s.batchCandidates(cfg, now)
		return at, !at.IsZero()
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
			if !batch.checksPassed || batch.ProvisionalFailure != "" {
				if deadline, enabled := batch.checkDeadline(cfg); enabled {
					consider(deadline.Add(time.Nanosecond))
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
