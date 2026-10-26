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
			if cfg := w.approvalStatus.Load(); cfg != nil {
				return *cfg, true
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.plannerConfig(s.options.PollInterval), true
		}
	}
	return queue.Config{}, false
}

// Called with exclusive worker ownership whenever configuration is replaced.
func (w *worker) publishApprovalStatus(interval time.Duration) {
	if w.stageA == nil {
		w.approvalStatus.Store(nil)
		return
	}
	cfg := w.plannerConfig(interval)
	w.approvalStatus.Store(&cfg)
}

func (w *worker) lockStatus() bool {
	if w.approvalStatus.Load() != nil {
		return w.mu.TryLock()
	}
	w.mu.Lock()
	return true
}

// The worker mutex serializes reporting and cache updates. Gates depend on
// current configuration and time and are always recalculated, never cached.
type ledgerCache struct {
	watermark  int64
	projection status.Queue
	journal    store.Journal
}

func (s *Server) ledgerSummary(ctx context.Context, w *worker, pr int) (string, error) {
	// Restored workers have no validated policy until configuration is loaded.
	// A real startup failure may publish, but must not advertise default limits.
	if w.managed && w.configDigest == "" {
		return "", nil
	}
	watermark, err := s.store.Watermark(ctx, w.id)
	if err != nil {
		return "", err
	}
	if w.ledgerCache == nil || w.ledgerCache.watermark != watermark {
		journals, err := s.store.ReportingJournals(ctx, []store.QueueKey{w.Key})
		if err != nil {
			return "", err
		}
		if len(journals) == 0 {
			return "", nil
		}
		projection, _, _ := status.Project(journals[0], 0, 0, s.options.Sanitizer)
		w.ledgerCache = &ledgerCache{watermark: projection.Watermark, projection: projection, journal: journals[0]}
	}
	q := &w.ledgerCache.projection
	q.Readiness = &status.Readiness{Ready: !w.managed || w.ready}
	if !q.Readiness.Ready {
		q.Readiness.Reason = w.readinessReason()
	}
	entry := q.LedgerEntry(pr)
	status.ApplyPolicy(q, entry, w.plannerConfig(s.options.PollInterval), time.Now())
	waiting := ""
	if entry != nil {
		waiting = "\n\nCurrent state: " + status.WaitingCheckText(q, *entry, s.options.Sanitizer)
	}
	return waiting + "\n\nQueue: " + status.AdaptiveSummary(q) + status.LedgerSummary(entry) + status.QueueInstructions(q), nil
}
