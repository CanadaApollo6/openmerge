package status

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func gitCacheRead(t *testing.T, h http.Handler) GitCacheSnapshot {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/git-cache", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body struct {
		Version int              `json:"version"`
		Cache   GitCacheSnapshot `json:"git_cache"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Version != Version {
		t.Fatalf("git cache read: %d %s (%v)", w.Code, w.Body, err)
	}
	if body.Cache.Since.IsZero() || body.Cache.Since.After(time.Now()) || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("missing timestamp or headers: %+v %v", body, w.Header())
	}
	return body.Cache
}

func TestGitCacheHistogramBoundaries(t *testing.T) {
	keys := []store.QueueKey{key}
	cache := NewGitCache(func() []store.QueueKey { return keys })
	h := Handler(nil, keys, "secret", nil, ReadOptions{GitCache: cache})
	bounds := []time.Duration{0, time.Microsecond, 10 * time.Microsecond, 100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second, 30 * time.Second, time.Minute}
	for i, upper := range bounds {
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			if upper+delta < 0 {
				continue
			}
			name := fmt.Sprintf("boundary-%d-%d", i, delta)
			cache.Observe(gitops.OperationStats{Repository: key.Repository, Operation: name, Wait: upper + delta, Hold: upper + delta, Maintenance: upper + delta})
			op := gitCacheRead(t, h).Repositories[key.Repository].Operations[name]
			var want GitCacheHistogram
			bucket := i
			if delta > 0 {
				bucket++
			}
			if bucket == len(bounds) {
				want.Overflow = 1
			} else {
				want.Buckets[bucket] = 1
			}
			if op.Count != 1 || op.Wait != want || op.Hold != want || op.Maintenance != want {
				t.Fatalf("duration %v: got %+v, want %+v", upper+delta, op, want)
			}
		}
	}
	if got := gitCacheRead(t, h).BucketUpperBoundsMS; got != [11]float64{0, .001, .01, .1, 1, 10, 100, 1000, 10000, 30000, 60000} {
		t.Fatal(got)
	}
}

func TestGitCacheGCAndDrops(t *testing.T) {
	keys := []store.QueueKey{key}
	cache := NewGitCache(func() []store.QueueKey { return keys })
	h := Handler(nil, keys, "secret", nil, ReadOptions{GitCache: cache})
	observe := func(s gitops.OperationStats) {
		s.Repository, s.Operation = key.Repository, "read-ref"
		cache.Observe(s)
	}
	observe(gitops.OperationStats{})
	r := gitCacheRead(t, h).Repositories[key.Repository]
	if r.LastGCAt != nil || r.LastCacheBytes != nil || r.Operations["read-ref"].Maintenance.Buckets[0] != 1 {
		t.Fatalf("unmeasured fields not null or zero timing lost: %+v", r)
	}
	observe(gitops.OperationStats{GCRan: true, GCOutcome: gitops.GCOK, CacheBytesMeasured: true, CacheBytes: 4096})
	before := time.Now().UTC()
	observe(gitops.OperationStats{GCRan: true, GCOutcome: gitops.GCFailed, Dropped: 7})
	failed := gitCacheRead(t, h).Repositories[key.Repository]
	if failed.GCOutcomes[gitops.GCFailed] != 1 || failed.GCOutcomes[gitops.GCOK] != 1 || failed.Dropped != 7 || failed.LastGCAt == nil || failed.LastGCAt.Before(before) || failed.LastCacheBytes == nil || *failed.LastCacheBytes != 4096 {
		t.Fatalf("GC failure or loss absent: %+v", failed)
	}
	observe(gitops.OperationStats{GCOutcome: gitops.GCSkippedBackoff})
	r = gitCacheRead(t, h).Repositories[key.Repository]
	if r.GCOutcomes[gitops.GCSkippedBackoff] != 1 || !r.LastGCAt.Equal(*failed.LastGCAt) || r.Dropped != 7 {
		t.Fatalf("backoff changed last attempt or recounted losses: %+v", r)
	}
	observe(gitops.OperationStats{GCRan: true, GCOutcome: gitops.GCTimedOut})
	if gitCacheRead(t, h).Repositories[key.Repository].GCOutcomes[gitops.GCTimedOut] != 1 {
		t.Fatal("timed-out attempt absent")
	}
}

func TestGitCacheReadContract(t *testing.T) {
	keys := []store.QueueKey{key}
	cache := NewGitCache(func() []store.QueueKey { return keys })
	cache.Observe(gitops.OperationStats{Repository: key.Repository, Operation: "read-ref"})
	cache.Observe(gitops.OperationStats{Repository: "other/private", Operation: "read-ref"})
	for name, h := range map[string]http.Handler{
		"static":  Handler(nil, keys, "secret", nil, ReadOptions{GitCache: cache}),
		"dynamic": DynamicHandler(nil, func() []store.QueueKey { return keys }, "secret", nil, ReadOptions{GitCache: cache}),
	} {
		t.Run(name, func(t *testing.T) {
			if got := gitCacheRead(t, h); len(got.Repositories) != 1 || got.Repositories[key.Repository].Operations["read-ref"].Count != 1 {
				t.Fatalf("wrong served inventory: %+v", got)
			}
			for _, token := range []string{"", "Bearer wrong"} {
				r := httptest.NewRequest("GET", "/v1/git-cache", nil)
				r.Header.Set("Authorization", token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
					t.Fatalf("token %q: %d %s", token, w.Code, w.Body)
				}
			}
			for _, method := range []string{"POST", "PUT", "DELETE", "HEAD", "PATCH", "OPTIONS"} {
				r := httptest.NewRequest(method, "/v1/git-cache", nil)
				r.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 405 || w.Header().Get("Allow") != "GET" {
					t.Fatalf("method %s: %d %s", method, w.Code, w.Body)
				}
			}
			for _, path := range []string{"/v1/git-cache?", "/v1/git-cache?repo=other/private", "/v1/git-cache?bad=%ZZ", "/v1/git-cache?;"} {
				r := httptest.NewRequest("GET", path, nil)
				r.Header.Set("Authorization", "Bearer secret")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 400 || !strings.Contains(w.Body.String(), `"invalid-request"`) {
					t.Fatalf("query %s: %d %s", path, w.Code, w.Body)
				}
			}
		})
	}
	if got := gitCacheRead(t, Handler(nil, nil, "secret", nil, ReadOptions{GitCache: cache})); len(got.Repositories) != 0 {
		t.Fatalf("request keys ignored: %+v", got)
	}
	if got := gitCacheRead(t, Handler(nil, keys, "secret", nil)); got.Repositories == nil || len(got.Repositories) != 0 {
		t.Fatalf("unconfigured cache: %+v", got)
	}
}

func TestGitCacheDiscoveryChurn(t *testing.T) {
	keys := []store.QueueKey{key}
	cache := NewGitCache(func() []store.QueueKey { return keys })
	h := DynamicHandler(nil, func() []store.QueueKey { return keys }, "secret", nil, ReadOptions{GitCache: cache})
	for i := range 1000 {
		keys = []store.QueueKey{{Repository: fmt.Sprintf("owner/repo-%d", i)}}
		cache.Observe(gitops.OperationStats{Repository: strings.ToUpper(keys[0].Repository), Operation: "read-ref"})
		cache.Observe(gitops.OperationStats{Repository: "other/private", Operation: "read-ref"})
		if len(cache.repositories) != 1 || len(gitCacheRead(t, h).Repositories) != 1 {
			t.Fatal("repository churn retained old aggregates")
		}
	}
	keys = nil
	if got := gitCacheRead(t, h); len(got.Repositories) != 0 || len(cache.repositories) != 0 {
		t.Fatalf("removed repository retained: %+v", got)
	}
	keys = []store.QueueKey{key, key}
	cache.Observe(gitops.OperationStats{Repository: key.Repository, Operation: "read-ref"})
	if got := gitCacheRead(t, h); len(got.Repositories) != 1 || got.Repositories[key.Repository].Operations["read-ref"].Count != 1 {
		t.Fatalf("readded repository did not restart: %+v", got)
	}
}

func TestGitCacheConcurrentObservations(t *testing.T) {
	keys := []store.QueueKey{key}
	cache := NewGitCache(func() []store.QueueKey { return keys })
	h := Handler(nil, keys, "secret", nil, ReadOptions{GitCache: cache})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 1000 {
				cache.Observe(gitops.OperationStats{Repository: key.Repository, Operation: "read-ref", Wait: time.Millisecond, GCRan: true, GCOutcome: gitops.GCFailed, CacheBytesMeasured: true, CacheBytes: 123, Dropped: 1})
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 100 {
				snapshot := gitCacheRead(t, h)
				if r, ok := snapshot.Repositories[key.Repository]; ok {
					r.GCOutcomes[gitops.GCFailed] = 0
					r.Operations["read-ref"] = GitCacheOperation{}
					*r.LastCacheBytes = 0
					*r.LastGCAt = time.Time{}
				}
			}
		})
	}
	wg.Wait()
	r := gitCacheRead(t, h).Repositories[key.Repository]
	op := r.Operations["read-ref"]
	if op.Count != 8000 || op.Wait.Buckets[4] != 8000 || r.Dropped != 8000 || r.GCOutcomes[gitops.GCFailed] != 8000 || *r.LastCacheBytes != 123 {
		t.Fatalf("concurrent observations lost: %+v", r)
	}
}
