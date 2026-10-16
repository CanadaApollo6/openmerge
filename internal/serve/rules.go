package serve

import (
	"context"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) repositoryRules(ctx context.Context, w *worker) error {
	if w.CheckRules == nil || time.Now().Before(w.rulesNextCheck) {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
	problems, warnings, err := w.CheckRules(readCtx)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, warning := range warnings {
		s.options.Logger.Warn("repository rules", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "diagnostic", warning)
	}
	if err != nil {
		w.rulesFailures++
		delay := min(time.Minute*time.Duration(1<<min(w.rulesFailures-1, 3)), 5*time.Minute, s.options.RulesInterval)
		w.rulesNextCheck = time.Now().Add(delay)
		s.options.Logger.Warn("repository rules read failed; keeping previous state", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "error", err, "retry_in", delay)
		return nil
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	if !slices.Equal(problems, state.RulesProblems()) {
		var event queue.Event = queue.RulesCleared{}
		if len(problems) > 0 {
			event = queue.RulesBlocked{Problems: problems}
		}
		if _, err := s.store.Append(ctx, w.id, []queue.Event{event}, ""); err != nil {
			return err
		}
	}
	w.rulesFailures = 0
	w.rulesNextCheck = time.Now().Add(s.options.RulesInterval)
	return nil
}
