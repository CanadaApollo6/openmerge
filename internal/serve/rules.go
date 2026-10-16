package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) repositoryRules(ctx context.Context, w *worker) error {
	if w.CheckRules == nil || time.Now().Before(w.rulesNextCheck) {
		return nil
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
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
	// Every failing read is recorded, even when the problems are unchanged, so it
	// advances the rules revision and invalidates any older read still in flight:
	// a stale pass can then never clear a block that a newer read confirmed. A
	// passing read is recorded only when it clears a block; a stale failing read can
	// at worst block a healthy queue until the next check, which fails safe.
	var event queue.Event
	switch {
	case len(problems) > 0:
		event = queue.RulesBlocked{Problems: problems, Basis: state.RulesRevision()}
	case len(state.RulesProblems()) > 0:
		event = queue.RulesCleared{Basis: state.RulesRevision()}
	}
	if event != nil {
		if _, err := s.store.Append(ctx, w.id, []queue.Event{event}, ""); err != nil {
			return err
		}
	}
	w.rulesFailures = 0
	w.rulesNextCheck = time.Now().Add(s.options.RulesInterval)
	return nil
}
