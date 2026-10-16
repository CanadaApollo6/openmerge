package serve

import (
	"context"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func (s *Server) repositoryRules(ctx context.Context, w *worker) error {
	if w.CheckRules == nil {
		return nil
	}
	if w.rulesChecked.IsZero() || time.Since(w.rulesChecked) >= s.options.RulesInterval {
		readCtx, cancel := context.WithTimeout(ctx, s.options.ActionTimeout)
		problems, warnings, err := w.CheckRules(readCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.rulesChecked = time.Now()
		for _, warning := range warnings {
			s.options.Logger.Warn("repository rules", "repository", w.Key.Repository, "base", w.Key.BaseBranch, "diagnostic", warning)
		}
		if err != nil {
			problems = append(problems, "Cannot validate repository rules: "+err.Error())
		}
		w.rulesProblem = strings.Join(problems, "; ")
	}
	return s.enforceRules(ctx, w)
}

func (s *Server) enforceRules(ctx context.Context, w *worker) error {
	if w.CheckRules == nil {
		return nil
	}
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return err
	}
	reason := state.PauseReason(w.plannerConfig(s.options.PollInterval))
	if w.rulesProblem != "" {
		// Preserve other pauses. Reconciliation or an explicit operator refresh
		// can clear them, then the cached rules failure takes effect before work.
		if reason == "" || reason == "repository-rules" {
			return s.pause(ctx, w, "repository-rules", w.rulesProblem)
		}
	} else if reason == "repository-rules" {
		_, err = s.store.Append(ctx, w.id, []queue.Event{queue.ResumeRequested{}}, "")
	}
	return err
}
