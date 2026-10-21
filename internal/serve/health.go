package serve

import (
	"context"
	"errors"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Health performs exactly one constant-cost DB probe and reads immutable worker
// snapshots. It never takes worker locks, scans journals or calls a forge.
func (s *Server) Health(ctx context.Context) (status.Health, error) {
	h := status.Health{Version: status.Version, Healthy: true, CheckedAt: time.Now(), Queues: []status.HealthQueue{}}
	if err := s.store.Probe(ctx); err != nil {
		return h, err
	}
	if !s.mu.TryRLock() {
		return h, errors.New("worker registry busy")
	}
	if len(s.queues) > status.MaxHealthQueues {
		s.mu.RUnlock()
		return h, errors.New("too many health queues")
	}
	workers := append([]*worker(nil), s.queues...)
	s.mu.RUnlock()
	for _, w := range workers {
		q := w.health.Load()
		if q == nil {
			q = &status.HealthQueue{Installation: w.Key.Installation, Repository: w.Key.Repository, Base: w.Key.BaseBranch, Lane: w.Key.Lane, Lifecycle: "unknown", Watched: true, StartedAt: s.startedAt, Readiness: status.Readiness{Reason: "not-yet-reconciled"}}
		}
		snapshot := *q
		if warnings := w.outsideWarnings.Load(); warnings != nil {
			snapshot.OutsideWarnings = status.ProjectOutsideWarnings(*warnings)
		}
		view := status.EvaluateHealth(snapshot, s.options.Health, h.CheckedAt)
		if view.Failing() {
			h.Healthy = false
		}
		h.Queues = append(h.Queues, view)
	}
	return h, nil
}

func (w *worker) publishDeadline(at time.Time) {
	for {
		old := w.health.Load()
		if old == nil {
			return
		}
		next := *old
		next.NextDeadline = &at
		next.InCycle = false
		next.CycleStartedAt = nil
		if w.health.CompareAndSwap(old, &next) {
			return
		}
	}
}

// Called under worker ownership. Successful report-only turns do not advance
// LastSuccess. No journal timestamp is used as reconciliation telemetry.
func (s *Server) publishHealth(w *worker, state queue.State, success bool, err error, now time.Time) {
	w.hasAuthorityPin.Store(state.AuthorityPin() != nil)
	q := status.HealthQueue{Installation: w.Key.Installation, Repository: w.Key.Repository, Base: w.Key.BaseBranch, Lane: w.Key.Lane, Lifecycle: state.LifecycleState(), StartedAt: now, Readiness: status.Readiness{Ready: !w.managed || w.ready}, Watched: true}
	if old := w.health.Load(); old != nil {
		q.StartedAt = old.StartedAt
		q.LastSuccess = old.LastSuccess
		q.Failures = old.Failures
		q.NextDeadline = old.NextDeadline
		q.InCycle = old.InCycle
		q.CycleStartedAt = old.CycleStartedAt
	}
	if !q.Readiness.Ready {
		q.Readiness.Reason = w.readinessReason()
	}
	pending := hasLandingObligations(state)
	if !pending && (w.settlementOnly || q.Lifecycle == "retired" || q.Lifecycle == "dormant") {
		q.Watched = false
	}
	if err != nil {
		q.Failures = min(q.Failures+1, 1000000)
	} else if success && w.rulesWaiting == "" {
		q.LastSuccess = &now
		q.Failures = 0
	}
	q.Failures = max(q.Failures, w.rulesFailures)
	if w.rulesWaiting != "" {
		q.Reasons = []string{"rules-validation-pending"}
	}
	q.Authority = status.ProjectAuthority(state, now)
	// The independent outside-push loop publishes ledger warnings without worker locks.
	w.health.Store(&q)
}

// QueueReadinessSnapshot uses TryLock for authority before approval activation.
// Legacy status retains its trial-era locking behavior.
func (s *Server) QueueReadinessSnapshot(key store.QueueKey) *status.Readiness {
	if !s.mu.TryRLock() {
		return &status.Readiness{Reason: "policy_busy"}
	}
	workers := append([]*worker(nil), s.queues...)
	s.mu.RUnlock()
	for _, w := range workers {
		if w.Key != key {
			continue
		}
		if !w.mu.TryLock() {
			return &status.Readiness{Reason: "policy_busy"}
		}
		r := &status.Readiness{Ready: !w.managed || w.ready}
		if !r.Ready {
			r.Reason = w.readinessReason()
		}
		w.mu.Unlock()
		return r
	}
	return nil
}

// QueuePolicySnapshot never waits behind a busy authority worker.
func (s *Server) QueuePolicySnapshot(key store.QueueKey) (queue.Config, bool) {
	if !s.mu.TryRLock() {
		return queue.Config{}, false
	}
	workers := append([]*worker(nil), s.queues...)
	s.mu.RUnlock()
	for _, w := range workers {
		if w.Key != key {
			continue
		}
		if cfg := w.approvalStatus.Load(); cfg != nil {
			return *cfg, true
		}
		if !w.mu.TryLock() {
			return queue.Config{}, false
		}
		cfg := w.plannerConfig(s.options.PollInterval)
		w.mu.Unlock()
		return cfg, true
	}
	return queue.Config{}, false
}

// Slot acquisition ends the next-start obligation, including the first turn.
func (w *worker) publishCycleStarted(at time.Time) {
	for {
		old := w.health.Load()
		if old == nil {
			return
		}
		next := *old
		next.NextDeadline = nil
		next.InCycle = true
		next.CycleStartedAt = &at
		if w.health.CompareAndSwap(old, &next) {
			return
		}
	}
}

// Recovery's terminal health state is explicit and independent of best-effort
// log replay. A failed log read must not leave an exited worker watched forever.
func (w *worker) publishRecoveryFinished() {
	for {
		old := w.health.Load()
		if old == nil {
			return
		}
		next := *old
		next.Watched = false
		next.InCycle = false
		next.CycleStartedAt = nil
		next.NextDeadline = nil
		if w.health.CompareAndSwap(old, &next) {
			return
		}
	}
}

func hasLandingObligations(state queue.State) bool {
	for _, b := range state.Batches() {
		if b.Phase == queue.Landing || b.LandingAttempt.ID != "" {
			return true
		}
	}
	return false
}
