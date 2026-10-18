package serve

import (
	"container/list"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// stageAGoverningCache holds only immutable commit-pinned governing content.
// Identity, membership and grants deliberately remain outside it and are read
// afresh at their policy/decision boundaries.
type stageAGoverningCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	lru      *list.List
	parsed   *classification.GoverningCache
}

type stageAGoverningEntry struct {
	key   string
	files forge.GoverningFiles
}

func newStageAGoverningCache() *stageAGoverningCache {
	return &stageAGoverningCache{
		capacity: 32,
		entries:  make(map[string]*list.Element),
		lru:      list.New(),
		parsed:   classification.NewGoverningCache(128),
	}
}

func (c *stageAGoverningCache) read(ctx context.Context, reader forge.GoverningFilesReader, repo forge.RepoRef, req forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	key := repo.Path + "\x00" + stageAGoverningKey(req)
	c.mu.Lock()
	if existing := c.entries[key]; existing != nil {
		c.lru.MoveToFront(existing)
		files := cloneStageAGoverning(existing.Value.(stageAGoverningEntry).files)
		c.mu.Unlock()
		return files, nil
	}
	c.mu.Unlock()

	files, err := reader.GoverningFiles(ctx, repo, req)
	if err != nil {
		return files, err
	}
	// Only fully known evidence is immutable cache material. An incomplete
	// response can reflect shallow history, a transient object read or a canceled
	// acquisition and must be retried at the same commit rather than becoming a
	// process-local durable fallback.
	if files.Coverage.Completeness != forge.DiffComplete {
		return files, nil
	}
	files = cloneStageAGoverning(files)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing := c.entries[key]; existing != nil {
		c.lru.MoveToFront(existing)
		return cloneStageAGoverning(existing.Value.(stageAGoverningEntry).files), nil
	}
	element := c.lru.PushFront(stageAGoverningEntry{key: key, files: files})
	c.entries[key] = element
	if c.lru.Len() > c.capacity {
		evicted := c.lru.Back()
		delete(c.entries, evicted.Value.(stageAGoverningEntry).key)
		c.lru.Remove(evicted)
	}
	return cloneStageAGoverning(files), nil
}

func (c *stageAGoverningCache) deriver(g forge.GoverningFiles, resolve classification.OwnerResolver) classification.TreeDeriver {
	return c.parsed.TreeDeriver(g, resolve)
}

func (c *stageAGoverningCache) owners(g forge.GoverningFiles) ([]classification.Owner, bool) {
	return c.parsed.GoverningOwners(g)
}

func stageAGoverningKey(req forge.GoverningFilesRequest) string {
	paths := slices.Clone(req.Paths)
	slices.Sort(paths)
	paths = slices.Compact(paths)
	return fmt.Sprintf("%s\x00%t\x00%t\x00%s", req.CommitSHA, req.PathsComplete, req.AllAttributes, strings.Join(paths, "\x00"))
}

func cloneStageAGoverning(in forge.GoverningFiles) forge.GoverningFiles {
	out := in
	out.Config = cloneStageAGoverningFiles(in.Config)
	out.Codeowners = cloneStageAGoverningFiles(in.Codeowners)
	out.Attributes = cloneStageAGoverningFiles(in.Attributes)
	out.Coverage.Reasons = slices.Clone(in.Coverage.Reasons)
	return out
}

func cloneStageAGoverningFiles(in []forge.GoverningFile) []forge.GoverningFile {
	out := slices.Clone(in)
	for i := range out {
		out[i].Coverage.Reasons = slices.Clone(in[i].Coverage.Reasons)
	}
	return out
}
