package status

import (
	"cmp"
	"slices"
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
	Installation   string                       `json:"installation"`
	Name           string                       `json:"name"`
	Operations     map[string]GitCacheOperation `json:"operations"`
	GCOutcomes     map[gitops.GCOutcome]uint64  `json:"gc_outcomes"`
	LastGCAt       *time.Time                   `json:"last_gc_at"`
	LastCacheBytes *int64                       `json:"last_cache_bytes"`
	Dropped        uint64                       `json:"dropped"`
}

// GitCacheSnapshot includes process initialization time so readers can calculate rates.
type GitCacheSnapshot struct {
	Since               time.Time                    `json:"since"`
	BucketUpperBoundsMS [len(gitCacheBounds)]float64 `json:"bucket_upper_bounds_ms"`
	Repositories        []GitCacheRepository         `json:"repositories"`
}

// gitCacheIdentity keeps installations apart because repository names alone do
// not confine a read: each installation has its own cache and its own served keys.
type gitCacheIdentity struct{ installation, name string }

func gitCacheKey(key store.QueueKey) gitCacheIdentity {
	return gitCacheIdentity{key.Installation, strings.ToLower(key.Repository)}
}

// GitCache receives samples on Repo workers so API reads cannot stall Git operations.
type GitCache struct {
	mu           sync.Mutex
	keys         func() []store.QueueKey
	repositories map[gitCacheIdentity]GitCacheRepository
}

// NewGitCache takes the served-key reader so discovery churn cannot retain unserved history.
func NewGitCache(keys func() []store.QueueKey) *GitCache {
	return &GitCache{keys: keys, repositories: make(map[gitCacheIdentity]GitCacheRepository)}
}

// Prune on observations too so a server with no API readers cannot retain removed identities.
func (c *GitCache) served() map[gitCacheIdentity]bool {
	served := make(map[gitCacheIdentity]bool)
	for _, key := range c.keys() {
		served[gitCacheKey(key)] = true
	}
	for identity := range c.repositories {
		if !served[identity] {
			delete(c.repositories, identity)
		}
	}
	return served
}

// Observe ignores unserved identities so diagnostic operations cannot expand API tenancy.
func (c *GitCache) Observe(installation string, s gitops.OperationStats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	identity := gitCacheKey(store.QueueKey{Installation: installation, Repository: s.Repository})
	if !c.served()[identity] {
		return
	}
	r, ok := c.repositories[identity]
	if !ok {
		r.Installation, r.Name = identity.installation, identity.name
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
	c.repositories[identity] = r
}

// Snapshot copies nested values so JSON encoding never holds the aggregation lock.
func (c *GitCache) Snapshot(keys []store.QueueKey) GitCacheSnapshot {
	result := GitCacheSnapshot{Since: processStarted, Repositories: []GitCacheRepository{}}
	for i, bound := range gitCacheBounds {
		result.BucketUpperBoundsMS[i] = float64(bound) / float64(time.Millisecond)
	}
	if c == nil {
		return result
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	served := c.served()
	copied := make(map[gitCacheIdentity]bool)
	for _, key := range keys {
		identity := gitCacheKey(key)
		if !served[identity] || copied[identity] {
			continue
		}
		copied[identity] = true
		r, ok := c.repositories[identity]
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
		result.Repositories = append(result.Repositories, r)
	}
	slices.SortFunc(result.Repositories, func(a, b GitCacheRepository) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Installation, b.Installation))
	})
	return result
}
