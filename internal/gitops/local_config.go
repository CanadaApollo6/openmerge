package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// LocalConfig reads immutable objects already in this repository's cache.
// It never opens a transport session, resolves credentials, fetches, creates a
// cache, updates refs, or runs maintenance. Missing objects fail closed.
func (r *Repo) LocalConfig(ctx context.Context, repo forge.RepoRef, commit string) ([]byte, error) {
	if !forge.ValidDiffObjectID(commit) {
		return nil, fmt.Errorf("local config requires full commit ID")
	}
	dir := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path))))
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !mu.TryLock() {
		return nil, diagnostic.BusyError("local config cache busy")
	}
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return nil, err
	}
	s := &session{r: r, dir: dir, diffRead: true}
	kind, err := s.command(ctx, "", "cat-file", "-t", commit)
	if err != nil || kind != "commit" {
		return nil, fmt.Errorf("local config commit unavailable")
	}
	_, data, err := config.Find(func(path string) ([]byte, bool, error) {
		raw, overflow, err := s.diffCommand(ctx, 4096, "ls-tree", "-z", "--full-tree", commit, "--", ":(literal)"+path)
		if err != nil || overflow {
			return nil, false, fmt.Errorf("local config tree unavailable")
		}
		if raw == "" {
			return nil, false, nil
		}
		header, name, ok := strings.Cut(strings.TrimSuffix(raw, "\x00"), "\t")
		fields := strings.Fields(header)
		if !ok || name != path || len(fields) != 3 || fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" || !forge.ValidDiffObjectID(fields[2]) {
			return nil, false, fmt.Errorf("invalid local config blob")
		}
		content, overflow, err := s.diffCommand(ctx, 1<<20, "cat-file", "blob", fields[2])
		if err != nil || overflow {
			return nil, false, fmt.Errorf("local config blob unavailable")
		}
		return []byte(content), true, nil
	})
	return data, err
}
