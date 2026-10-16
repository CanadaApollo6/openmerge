package github

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

type cacheKey struct {
	url            string
	token, headers [sha256.Size]byte
}

type cachedResponse struct {
	key          cacheKey
	header       http.Header
	body         []byte
	uncompressed bool
	size         int
}

// responseCache stores immutable snapshots. A caller keeps its snapshot across
// the network request, even if another request replaces or evicts that entry.
type responseCache struct {
	mu                   sync.Mutex
	entries              map[cacheKey]*list.Element
	lru                  list.List
	bytes                int
	maxEntries, maxBytes int
}

func newResponseCache(entries, bytes int) *responseCache {
	return &responseCache{entries: make(map[cacheKey]*list.Element), maxEntries: entries, maxBytes: bytes}
}

func (c *responseCache) get(key cacheKey) *cachedResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		c.lru.MoveToFront(e)
		return e.Value.(*cachedResponse)
	}
	return nil
}

func (c *responseCache) remove(e *list.Element) {
	value := e.Value.(*cachedResponse)
	delete(c.entries, value.key)
	c.bytes -= value.size
	c.lru.Remove(e)
}

func (c *responseCache) put(key cacheKey, value *cachedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		c.remove(e)
	}
	if value == nil || value.size > c.maxBytes || c.maxEntries <= 0 {
		return
	}
	c.entries[key] = c.lru.PushFront(value)
	c.bytes += value.size
	for c.lru.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.remove(c.lru.Back())
	}
}

// conditionalGET always contacts GitHub. Only a validated 304 replays data; an
// error never falls back to a cached authorization or CI result.
func (t *transport) conditionalGET(req *http.Request, token string) (*http.Response, error) {
	if t.cache == nil || req.Method != http.MethodGet || req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" || req.Header.Get("Range") != "" {
		return t.base.RoundTrip(req)
	}
	headers := req.Header.Clone()
	headers.Del("Authorization")
	digest := sha256.New()
	_ = headers.Write(digest) // Hash writes cannot fail; Header.Write sorts keys.
	key := cacheKey{url: req.URL.String(), token: sha256.Sum256([]byte(token)), headers: [sha256.Size]byte(digest.Sum(nil))}
	cached := t.cache.get(key)
	if cached != nil {
		req.Header.Set("If-None-Match", cached.header.Get("ETag"))
	}
	res, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotModified {
		_ = res.Body.Close()
		if cached == nil {
			return nil, errors.New("github returned 304 without a cached representation")
		}
		header := cached.header.Clone()
		for name, values := range res.Header {
			header[name] = append([]string(nil), values...)
		}
		res.StatusCode, res.Status = http.StatusOK, "200 OK"
		res.Header, res.Body = header, io.NopCloser(bytes.NewReader(cached.body))
		res.ContentLength = int64(len(cached.body))
		res.Header.Set("Content-Length", strconv.Itoa(len(cached.body)))
		res.TransferEncoding, res.Uncompressed = nil, cached.uncompressed
		return res, nil
	}
	// The key covers every explicit request header, including all Vary dimensions.
	cacheable := res.StatusCode == http.StatusOK && res.Header.Get("ETag") != "" && !strings.Contains(strings.ToLower(res.Header.Get("Cache-Control")), "no-store")
	for _, vary := range strings.Split(res.Header.Get("Vary"), ",") {
		if strings.TrimSpace(vary) == "*" {
			cacheable = false
		}
	}
	if !cacheable {
		t.cache.put(key, nil)
		return res, nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, int64(t.cache.maxBytes)+1))
	if err != nil {
		_ = res.Body.Close()
		return nil, err
	}
	if len(body) > t.cache.maxBytes {
		t.cache.put(key, nil)
		res.Body = &prefixBody{Reader: io.MultiReader(bytes.NewReader(body), res.Body), Closer: res.Body}
		return res, nil
	}
	_ = res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(body))
	value := &cachedResponse{key: key, header: res.Header.Clone(), body: body, uncompressed: res.Uncompressed}
	value.size = len(body) + len(key.url) + 2*sha256.Size
	for name, values := range value.header {
		value.size += len(name)
		for _, v := range values {
			value.size += len(v)
		}
	}
	t.cache.put(key, value)
	return res, nil
}
