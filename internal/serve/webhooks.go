package serve

import (
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Delivery observations are scheduling hints scoped to this listener lifetime.
// They never enter the journal or authorize a queue transition.
type webhookObservations struct {
	mu            sync.RWMutex
	installations map[string]time.Time
	repositories  map[string]time.Time
}

func (h *webhookObservations) observe(installation, repository string, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.installations == nil {
		h.installations = make(map[string]time.Time)
		h.repositories = make(map[string]time.Time)
	}
	h.installations[installation] = now
	if repository != "" {
		h.repositories[installation+"/"+strings.ToLower(repository)] = now
	}
}

func (h *webhookObservations) snapshot(installation, repository string) (time.Time, time.Time) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.installations[installation], h.repositories[installation+"/"+strings.ToLower(repository)]
}

func (s *Server) webhookHealthy(last, now time.Time) bool {
	return s.options.WebhooksConfigured && !last.IsZero() && now.Before(last.Add(s.options.WebhookHealthWindow))
}

func (s *Server) pollDelay(installation, repository string, state queue.State, now time.Time) time.Duration {
	if needsActivePolling(state) {
		return s.options.PollInterval
	}
	last, repoLast := s.webhooks.snapshot(installation, repository)
	if !s.webhookHealthy(last, now) || !s.webhookHealthy(repoLast, now) {
		return s.options.PollInterval
	}
	// Revisit health at expiry even when the configured backstop is longer.
	return min(s.options.BackstopInterval, last.Add(s.options.WebhookHealthWindow).Sub(now), repoLast.Add(s.options.WebhookHealthWindow).Sub(now))
}

func needsActivePolling(state queue.State) bool {
	for _, batch := range state.Batches() {
		if batch.Phase == queue.Testing || batch.Phase == queue.Landing || batch.LandingAttempt.ID != "" {
			return true
		}
	}
	return false
}
