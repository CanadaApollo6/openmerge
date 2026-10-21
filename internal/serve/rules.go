package serve

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) repositoryRules(ctx context.Context, w *worker) error {
	ctx = forge.WithRequestClass(ctx, forge.RulesRequest)
	if w.CheckRules == nil && w.CheckProtection == nil && w.CheckPermissions == nil || time.Now().Before(w.rulesNextCheck) {
		return nil
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	type ruleCheck struct {
		source queue.RulesSource
		read   func(context.Context) ([]string, []string, error)
	}
	checks := []ruleCheck{
		{queue.Rulesets, w.CheckRules},
		{queue.Permissions, w.CheckPermissions},
	}
	if w.CheckProtection != nil {
		checks = append(checks, ruleCheck{queue.ClassicProtection, func(ctx context.Context) ([]string, []string, error) {
			return w.CheckProtection(ctx, forge.MergeMethod(w.cfg.MergeMethod))
		}})
	}
	var events []queue.Event
	failed := false
	var quota error
	var quotaUntil time.Time
	w.rulesWaiting = ""
	for _, check := range checks {
		if check.source == queue.WriteRestriction || check.read == nil {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
		problems, warnings, readErr := check.read(readCtx)
		if readErr == nil {
			w.publishProgress(time.Now())
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, warning := range warnings {
			s.options.Logger.Warn("repository rules", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "source", check.source, "diagnostic", warning)
		}
		previous := state.RulesSourceProblems(check.source)
		if errors.Is(readErr, forge.ErrRulesUnavailable) && len(problems) == 0 {
			// A permanent permission gap is not a failed check: retrying cannot
			// fix it, and gating activation on it would wedge the queue.
			s.options.Logger.Warn("repository rules unavailable; keeping previous state for source", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "source", check.source, "error", readErr)
			continue
		}
		if readErr != nil {
			failed = true
			var rate interface{ RetryAfter() time.Time }
			if errors.As(readErr, &rate) && rate.RetryAfter().After(quotaUntil) {
				quota, quotaUntil = readErr, rate.RetryAfter()
			}
			w.rulesWaiting = "repository rules not yet verified: " + string(check.source) + " read failed"
			s.options.Logger.Warn("repository rules read failed; keeping previous state for source", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "source", check.source, "error", readErr)
			if len(problems) == 0 {
				continue
			}
			// Partial reads can add definite blocks but cannot remove any of this
			// source's previous findings. Other sources remain independent.
			problems = append(slices.Clone(problems), previous...)
			slices.Sort(problems)
			problems = slices.Compact(problems)
		}
		basis := state.RulesSourceRevision(check.source)
		switch {
		case len(problems) > 0:
			for _, problem := range problems {
				s.options.Logger.Error("repository rules block landing", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "source", check.source, "diagnostic", problem)
			}
			// Every definite failure invalidates older in-flight passes, even if
			// unchanged or based on a stale revision. Clears require this basis.
			events = append(events, queue.RulesSourceBlocked{Source: check.source, Problems: problems, Basis: basis})
		case len(previous) > 0:
			events = append(events, queue.RulesSourceCleared{Source: check.source, Basis: basis})
		}
	}
	if len(events) > 0 {
		if _, err := s.store.Append(ctx, w.id, events, ""); err != nil {
			return err
		}
	}
	if failed {
		w.rulesFailures++
		delay := min(time.Minute*time.Duration(1<<min(w.rulesFailures-1, 3)), 5*time.Minute, s.options.RulesInterval)
		w.rulesNextCheck = time.Now().Add(delay)
	} else {
		w.rulesFailures = 0
		w.rulesNextCheck = time.Now().Add(s.options.RulesInterval)
	}
	if quota != nil {
		w.rulesNextCheck = quotaUntil
		if w.managed && !w.ready {
			w.waiting = w.rulesWaiting
			w.validationNext = quotaUntil
		}
		return quota
	}
	return nil
}
