package gitops

import (
	"context"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// GCOutcome describes collection only. An empty outcome means maintenance
// did not reach a due GC.
type GCOutcome string

// GC outcomes distinguish successful, failed, expired, and deferred attempts.
const (
	GCOK             GCOutcome = "ok"
	GCFailed         GCOutcome = "failed"
	GCTimedOut       GCOutcome = "timed-out"
	GCSkippedBackoff GCOutcome = "skipped-backoff"
)

// OperationStats describes one successful acquisition, including error paths.
// Repository is the forge's repository identity (owner/name), never a cache path.
// Hold excludes maintenance. CacheBytes is valid only when CacheBytesMeasured.
type OperationStats struct {
	Operation          string
	Repository         string
	Wait               time.Duration
	Hold               time.Duration
	Maintenance        time.Duration
	GCRan              bool
	GC                 time.Duration
	GCOutcome          GCOutcome
	CacheBytes         int64
	CacheBytesMeasured bool
	// Dropped counts samples discarded by this Repo since the prior delivery.
	Dropped uint64
}

type operationObserver struct {
	mu      sync.Mutex
	queue   chan OperationStats
	running bool
	dropped uint64
}

func (r *Repo) observeOperation(stats OperationStats) {
	o := &r.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.queue == nil {
		o.queue = make(chan OperationStats, 256)
	}
	select {
	case o.queue <- stats:
	default:
		o.dropped++
	}
	if !o.running {
		o.running = true
		go r.deliverOperations()
	}
}

func (r *Repo) deliverOperations() {
	o := &r.observer
	for {
		o.mu.Lock()
		var stats OperationStats
		select {
		case stats = <-o.queue:
			stats.Dropped, o.dropped = o.dropped, 0
		default:
			// Serialize idle exit with enqueue so a sample cannot be stranded.
			o.running = false
			o.mu.Unlock()
			return
		}
		o.mu.Unlock()
		func() {
			defer func() { _ = recover() }()
			r.cfg.Observe(stats)
		}()
	}
}

type cacheLockMode int

const (
	cachePoll cacheLockMode = iota
	cacheBlocking
	cacheTry
)

type cacheGuard struct {
	mu       *sync.Mutex
	r        *Repo
	stats    *OperationStats
	acquired time.Time
}

func (r *Repo) acquireCache(ctx context.Context, dir string, repo forge.RepoRef, operation string, mode cacheLockMode) (*cacheGuard, error) {
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	g := &cacheGuard{mu: lock.(*sync.Mutex), r: r}
	var started time.Time
	if r.cfg.Observe != nil {
		started = time.Now()
	}
	switch mode {
	case cacheBlocking:
		g.mu.Lock()
	case cacheTry:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !g.mu.TryLock() {
			return nil, diagnostic.BusyError("local config cache busy")
		}
	default:
		if err := lockCache(ctx, g.mu); err != nil {
			return nil, err
		}
	}
	if r.cfg.Observe != nil {
		g.acquired = time.Now()
		g.stats = &OperationStats{Operation: operation, Repository: repo.Path, Wait: g.acquired.Sub(started)}
	}
	return g, nil
}

// A nil session preserves local readers' and failed opens' no-maintenance policy.
func (g *cacheGuard) release(s *session) {
	defer func() {
		g.mu.Unlock()
		if g.stats != nil {
			g.r.observeOperation(*g.stats)
		}
	}()
	if g.stats != nil {
		g.stats.Hold = time.Since(g.acquired)
	}
	if s != nil {
		var started time.Time
		if g.stats != nil {
			started = time.Now()
		}
		timeout := g.r.maintenanceTimeout
		if timeout == 0 {
			timeout = MaintenanceTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		_ = s.maintainCache(ctx, g.stats)
		if g.stats != nil {
			g.stats.Maintenance = time.Since(started)
		}
	}
}
