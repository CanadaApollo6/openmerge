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
	if w.CheckRules == nil && w.CheckProtection == nil && len(w.AdditionalRuleChecks) == 0 || time.Now().Before(w.rulesNextCheck) {
		return nil
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	var problems []string
	var readErrors []error
	checks := append([]func(context.Context) ([]string, []string, error){w.CheckRules}, w.AdditionalRuleChecks...)
	if w.CheckProtection != nil {
		checks = append(checks, func(ctx context.Context) ([]string, []string, error) {
			return w.CheckProtection(ctx, forge.MergeMethod(w.cfg.MergeMethod))
		})
	}
	for _, check := range checks {
		if check == nil {
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
		found, warnings, readErr := check(readCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, warning := range warnings {
			s.options.Logger.Warn("repository rules", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "diagnostic", warning)
		}
		if readErr != nil {
			readErrors = append(readErrors, readErr)
		} else {
			problems = append(problems, found...)
		}
	}
	if len(readErrors) > 0 {
		w.rulesFailures++
		delay := min(time.Minute*time.Duration(1<<min(w.rulesFailures-1, 3)), 5*time.Minute, s.options.RulesInterval)
		w.rulesNextCheck = time.Now().Add(delay)
		s.options.Logger.Warn("repository rules read failed; keeping previous state", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", errors.Join(readErrors...), "retry_in", delay)
		if len(problems) == 0 {
			return nil
		}
		// A readable source may add a block, but no partial pass can clear any
		// previous problem. Clear only after every source is readable and passes.
		problems = append(problems, state.RulesProblems()...)
		slices.Sort(problems)
		problems = slices.Compact(problems)
	}
	// Every failing read is recorded, even when the problems are unchanged, so it
	// advances the rules revision and invalidates any older read still in flight:
	// a stale pass can then never clear a block that a newer read confirmed. A
	// passing read is recorded only when it clears a block; a stale failing read can
	// at worst block a healthy queue until the next check, which fails safe.
	var event queue.Event
	switch {
	case len(problems) > 0:
		for _, problem := range problems {
			s.options.Logger.Error("repository rules block landing", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "diagnostic", problem)
		}
		event = queue.RulesBlocked{Problems: problems, Basis: state.RulesRevision()}
	case len(state.RulesProblems()) > 0:
		event = queue.RulesCleared{Basis: state.RulesRevision()}
	}
	if event != nil {
		if _, err := s.store.Append(ctx, w.id, []queue.Event{event}, ""); err != nil {
			return err
		}
	}
	if len(readErrors) == 0 {
		w.rulesFailures = 0
		w.rulesNextCheck = time.Now().Add(s.options.RulesInterval)
	}
	return nil
}
