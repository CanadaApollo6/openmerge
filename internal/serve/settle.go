package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// settle runs before notifications, configuration, rules, and PR reads. It only
// claims fence resolution; stale reporting/cleanup actions cannot delay it.
func (s *Server) settle(ctx context.Context, w *worker) (bool, error) {
	state, _, err := s.store.Load(ctx, w.id)
	if err != nil {
		return true, err
	}
	if state.Active == nil || state.Active.Phase != queue.Landing {
		return false, nil
	}
	cfg := w.plannerConfig(s.options.PollInterval)
	if _, err := s.store.Plan(ctx, w.id, cfg, time.Now()); err != nil {
		return true, err
	}
	claims, err := s.store.ClaimSettlement(ctx, w.id, s.options.ActionTimeout+time.Minute, time.Now())
	if err != nil {
		return true, err
	}
	if len(claims) != 0 {
		if err := s.execute(ctx, w, claims[0]); err != nil {
			return true, err
		}
	}
	state, _, err = s.store.Load(ctx, w.id)
	return state.Active != nil && state.Active.Phase == queue.Landing, err
}
