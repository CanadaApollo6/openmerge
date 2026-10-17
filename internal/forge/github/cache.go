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
func (t *transport) conditionalGET(req *http.Request, token string, revalidated func()) (*http.Response, error) {
	if t.cache == nil || req.Method != http.MethodGet || req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "" || req.Header.Get("Range") != "" {
		return t.base.RoundTrip(req)
	}
	headers := req.Header.Clone()
	headers.Del("Authorization")
	digest := sha256.New()
	_ = headers.Write(digest) // Hash writes cannot fail; Header.Write sorts keys.
	identity := sha256.Sum256([]byte(token))
	// JWT renewal does not change this App transport's authorization identity.
	// Installation-token caches remain token-scoped as grants can change.
	if t.appIdentity {
		identity = sha256.Sum256([]byte("app"))
	}
	key := cacheKey{url: req.URL.String(), token: identity, headers: [sha256.Size]byte(digest.Sum(nil))}
	cached := t.cache.get(key)
	if cached != nil {
		req.Header.Set("If-None-Match", cached.header.Get("ETag"))
	}
	res, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusNotModified {
		revalidated()
		_ = res.Body.Close()
		if cached == nil {
			return nil, errors.New("github returned 304 without a cached representation")
		}
		tag, valid := entityTag(res.Header)
		// If-None-Match uses weak comparison, but still requires a valid tag.
		if !valid || strings.TrimPrefix(tag, "W/") != strings.TrimPrefix(cached.header.Get("ETag"), "W/") {
			t.cache.put(key, nil)
			return nil, errors.New("github returned 304 with a missing, invalid, or mismatched ETag")
		}
		header := cached.header.Clone()
		for name, values := range res.Header {
			if name == "Content-Length" || name == "Content-Range" || cached.uncompressed && name == "Content-Encoding" {
				continue
			}
			header[name] = append([]string(nil), values...)
		}
		if !cacheableHeader(header) {
			t.cache.put(key, nil)
			return nil, errors.New("github returned 304 with uncacheable metadata")
		}
		header.Set("ETag", tag)
		t.cache.put(key, newCachedResponse(key, header, cached.body, cached.uncompressed))
		res.StatusCode, res.Status = http.StatusOK, "200 OK"
		res.Header, res.Body = header, io.NopCloser(bytes.NewReader(cached.body))
		res.ContentLength = int64(len(cached.body))
		res.Header.Set("Content-Length", strconv.Itoa(len(cached.body)))
		res.TransferEncoding, res.Uncompressed = nil, cached.uncompressed
		return res, nil
	}
	// The key covers every explicit request header, including all Vary dimensions.
	if res.StatusCode != http.StatusOK || !cacheableHeader(res.Header) {
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
	header := res.Header.Clone()
	tag, _ := entityTag(header)
	header.Set("ETag", tag)
	t.cache.put(key, newCachedResponse(key, header, body, res.Uncompressed))
	return res, nil
}

func newCachedResponse(key cacheKey, header http.Header, body []byte, uncompressed bool) *cachedResponse {
	value := &cachedResponse{key: key, header: header.Clone(), body: body, uncompressed: uncompressed}
	value.size = len(body) + len(key.url) + 2*sha256.Size
	for name, values := range value.header {
		value.size += len(name)
		for _, v := range values {
			value.size += len(v)
		}
	}
	return value
}

// RFC 9110 section 8.8.3 permits one quoted opaque tag, optionally prefixed W/.
func entityTag(header http.Header) (string, bool) {
	values := header.Values("ETag")
	if len(values) != 1 {
		return "", false
	}
	tag := strings.Trim(values[0], " \t")
	opaque := strings.TrimPrefix(tag, "W/")
	if len(opaque) < 2 || opaque[0] != '"' || opaque[len(opaque)-1] != '"' {
		return "", false
	}
	for i := 1; i < len(opaque)-1; i++ {
		if opaque[i] < 0x21 || opaque[i] == '"' || opaque[i] == 0x7f {
			return "", false
		}
	}
	return tag, true
}

func cacheableHeader(header http.Header) bool {
	if _, valid := entityTag(header); !valid {
		return false
	}
	for _, line := range header.Values("Cache-Control") {
		for _, value := range strings.Split(line, ",") {
			name, _, _ := strings.Cut(value, "=")
			if strings.EqualFold(strings.TrimSpace(name), "no-store") {
				return false
			}
		}
	}
	for _, line := range header.Values("Vary") {
		for _, value := range strings.Split(line, ",") {
			if strings.TrimSpace(value) == "*" {
				return false
			}
		}
	}
	return true
}
