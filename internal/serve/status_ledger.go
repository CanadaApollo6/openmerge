package serve

import (
	"context"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// QueuePolicy exposes the current adaptive policy to read-only status clients.
func (s *Server) QueuePolicy(key store.QueueKey) (queue.Config, bool) {
	for _, w := range s.workers() {
		if w.Key == key {
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.plannerConfig(s.options.PollInterval), true
		}
	}
	return queue.Config{}, false
}

// The worker mutex serializes reporting and cache updates. Gates depend on
// current configuration and time and are always recalculated, never cached.
type ledgerCache struct {
	watermark  int64
	projection status.Queue
}

func (s *Server) ledgerSummary(ctx context.Context, w *worker, pr int) (string, error) {
	watermark, err := s.store.Watermark(ctx, w.id)
	if err != nil {
		return "", err
	}
	if w.ledgerCache == nil || w.ledgerCache.watermark != watermark {
		journals, err := s.store.Journals(ctx, []store.QueueKey{w.Key})
		if err != nil {
			return "", err
		}
		if len(journals) == 0 {
			return "", nil
		}
		projection, _, _ := status.Project(journals[0], 0, 0, nil)
		w.ledgerCache = &ledgerCache{watermark: projection.Watermark, projection: projection}
	}
	q := &w.ledgerCache.projection
	q.Readiness = &status.Readiness{Ready: !w.managed || w.ready}
	if !q.Readiness.Ready {
		q.Readiness.Reason = w.readinessReason()
	}
	entry := q.LedgerEntry(pr)
	status.ApplyPolicy(q, entry, w.plannerConfig(s.options.PollInterval), time.Now())
	return "\n\nQueue: " + status.AdaptiveSummary(q) + status.LedgerSummary(entry), nil
}
