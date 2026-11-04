package status

import (
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

var processStarted = time.Now().UTC()

var gitCacheBounds = [...]time.Duration{0, time.Microsecond, 10 * time.Microsecond, 100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second, 30 * time.Second, time.Minute}

// GitCacheHistogram retains counters rather than samples to bound memory.
type GitCacheHistogram struct {
	Buckets  [len(gitCacheBounds)]uint64 `json:"buckets"`
	Overflow uint64                      `json:"overflow"`
}

func (h *GitCacheHistogram) observe(d time.Duration) {
	for i, bound := range gitCacheBounds {
		if d <= bound {
			h.Buckets[i]++
			return
		}
	}
	h.Overflow++
}

// GitCacheOperation separates hold and maintenance so GC is not attributed to Git work.
type GitCacheOperation struct {
	Count       uint64            `json:"count"`
	Wait        GitCacheHistogram `json:"wait_ms"`
	Hold        GitCacheHistogram `json:"hold_ms"`
	Maintenance GitCacheHistogram `json:"maintenance_ms"`
}

// GitCacheRepository uses nullable measurements to distinguish unmeasured and empty caches.
type GitCacheRepository struct {
	Operations     map[string]GitCacheOperation `json:"operations"`
	GCOutcomes     map[gitops.GCOutcome]uint64  `json:"gc_outcomes"`
	LastGCAt       *time.Time                   `json:"last_gc_at"`
	LastCacheBytes *int64                       `json:"last_cache_bytes"`
	Dropped        uint64                       `json:"dropped"`
}

// GitCacheSnapshot includes process initialization time so readers can calculate rates.
type GitCacheSnapshot struct {
	Since               time.Time                     `json:"since"`
	BucketUpperBoundsMS [len(gitCacheBounds)]float64  `json:"bucket_upper_bounds_ms"`
	Repositories        map[string]GitCacheRepository `json:"repositories"`
}

// GitCache receives samples on Repo workers so API reads cannot stall Git operations.
type GitCache struct {
	mu           sync.Mutex
	keys         func() []store.QueueKey
	repositories map[string]GitCacheRepository
}

// NewGitCache takes the served-key reader so discovery churn cannot retain unserved history.
func NewGitCache(keys func() []store.QueueKey) *GitCache {
	return &GitCache{keys: keys, repositories: make(map[string]GitCacheRepository)}
}

// Prune on observations too so a server with no API readers cannot retain removed identities.
func (c *GitCache) served() map[string]bool {
	served := make(map[string]bool)
	for _, key := range c.keys() {
		served[strings.ToLower(key.Repository)] = true
	}
	for name := range c.repositories {
		if _, ok := served[name]; !ok {
			delete(c.repositories, name)
		}
	}
	return served
}

// Observe ignores unserved identities so diagnostic operations cannot expand API tenancy.
func (c *GitCache) Observe(s gitops.OperationStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	served := c.served()
	name := strings.ToLower(s.Repository)
	if _, ok := served[name]; !ok {
		return
	}
	r, ok := c.repositories[name]
	if !ok {
		r.Operations = make(map[string]GitCacheOperation)
		r.GCOutcomes = map[gitops.GCOutcome]uint64{gitops.GCOK: 0, gitops.GCFailed: 0, gitops.GCTimedOut: 0, gitops.GCSkippedBackoff: 0}
	}
	op := r.Operations[s.Operation]
	op.Count++
	op.Wait.observe(s.Wait)
	op.Hold.observe(s.Hold)
	op.Maintenance.observe(s.Maintenance)
	r.Operations[s.Operation] = op
	if _, known := r.GCOutcomes[s.GCOutcome]; known {
		r.GCOutcomes[s.GCOutcome]++
	}
	if s.GCRan {
		now := time.Now().UTC()
		r.LastGCAt = &now
	}
	if s.CacheBytesMeasured {
		value := s.CacheBytes
		r.LastCacheBytes = &value
	}
	r.Dropped += s.Dropped
	c.repositories[name] = r
}

// Snapshot copies nested values so JSON encoding never holds the aggregation lock.
func (c *GitCache) Snapshot(keys []store.QueueKey) GitCacheSnapshot {
	result := GitCacheSnapshot{Since: processStarted, Repositories: make(map[string]GitCacheRepository)}
	for i, bound := range gitCacheBounds {
		result.BucketUpperBoundsMS[i] = float64(bound) / float64(time.Millisecond)
	}
	if c == nil {
		return result
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	served := c.served()
	for _, key := range keys {
		name := strings.ToLower(key.Repository)
		if _, ok := served[name]; !ok {
			continue
		}
		if _, copied := result.Repositories[name]; copied {
			continue
		}
		r, ok := c.repositories[name]
		if !ok {
			continue
		}
		ops := make(map[string]GitCacheOperation, len(r.Operations))
		for op, stats := range r.Operations {
			ops[op] = stats
		}
		outcomes := make(map[gitops.GCOutcome]uint64, len(r.GCOutcomes))
		for outcome, count := range r.GCOutcomes {
			outcomes[outcome] = count
		}
		r.Operations, r.GCOutcomes = ops, outcomes
		if r.LastGCAt != nil {
			at := *r.LastGCAt
			r.LastGCAt = &at
		}
		if r.LastCacheBytes != nil {
			value := *r.LastCacheBytes
			r.LastCacheBytes = &value
		}
		result.Repositories[name] = r
	}
	return result
}
