package classification

import (
	"container/list"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// GoverningCache retains parse products for immutable governing blobs. Object
// IDs, rather than branch names or mutable paths, are the cache identity. The
// cache never contains identity, membership, or grant observations.
type GoverningCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	lru      *list.List
}

type governingCacheEntry struct {
	key   string
	owner ownersFile
	attrs attributesFile
}

// NewGoverningCache returns a bounded LRU. A nonpositive size disables cache
// retention while preserving the same parse behavior.
func NewGoverningCache(capacity int) *GoverningCache {
	return &GoverningCache{capacity: capacity, entries: make(map[string]*list.Element), lru: list.New()}
}

func (c *GoverningCache) owner(blob, content string) ownersFile {
	key := "owners\x00" + blob
	return c.get(key, func(entry *governingCacheEntry) { entry.owner = parseOwners(content) }).owner
}

func (c *GoverningCache) attributes(blob, content string) attributesFile {
	key := "attributes\x00" + blob
	return c.get(key, func(entry *governingCacheEntry) { entry.attrs = parseAttributes(content) }).attrs
}

func (c *GoverningCache) get(key string, parse func(*governingCacheEntry)) governingCacheEntry {
	if c == nil || c.capacity <= 0 {
		entry := governingCacheEntry{key: key}
		parse(&entry)
		return entry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.entries[key]; existing != nil {
		c.lru.MoveToFront(existing)
		return existing.Value.(governingCacheEntry)
	}
	entry := governingCacheEntry{key: key}
	parse(&entry)
	element := c.lru.PushFront(entry)
	c.entries[key] = element
	if c.lru.Len() > c.capacity {
		evicted := c.lru.Back()
		delete(c.entries, evicted.Value.(governingCacheEntry).key)
		c.lru.Remove(evicted)
	}
	return entry
}

// TreeDeriver prepares a no-I/O deriver backed by this immutable parse cache.
func (c *GoverningCache) TreeDeriver(g forge.GoverningFiles, resolve OwnerResolver) TreeDeriver {
	d := TreeDeriver{Resolve: resolve, owners: map[string]ownersFile{}, attributes: map[string]attributesFile{}}
	for _, f := range g.Codeowners {
		if f.Present && definite(f) {
			d.owners[f.Path] = c.owner(f.ObjectID, f.Content)
		}
	}
	for _, f := range g.Attributes {
		if f.Present && definite(f) {
			d.attributes[f.Path] = c.attributes(f.ObjectID, f.Content)
		}
	}
	return d
}

// GoverningOwners applies the canonical CODEOWNERS parser through the cache.
// It preserves the same completeness and syntax contract as GoverningOwners.
func (c *GoverningCache) GoverningOwners(g forge.GoverningFiles) ([]Owner, bool) {
	source, known := selectedOwners(g)
	if !known {
		return nil, false
	}
	if !source.Present {
		return []Owner{}, true
	}
	if !forge.ValidDiffObjectID(source.ObjectID) {
		return nil, false
	}
	parsed := c.owner(source.ObjectID, source.Content)
	if !parsed.valid {
		return nil, false
	}
	return append([]Owner(nil), parsed.tokens...), true
}
