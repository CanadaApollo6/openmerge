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

// localPolicySession opens only an existing local cache and immutable commit.
// It never resolves credentials, fetches, updates refs, or runs maintenance.
func (r *Repo) localPolicySession(ctx context.Context, repo forge.RepoRef, commit string) (*session, func(), error) {
	if !forge.ValidDiffObjectID(commit) {
		return nil, nil, fmt.Errorf("local config requires full commit ID")
	}
	dir := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path))))
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !mu.TryLock() {
		return nil, nil, diagnostic.BusyError("local config cache busy")
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		mu.Unlock()
		return nil, nil, err
	}
	s := &session{r: r, dir: dir, diffRead: true}
	kind, err := s.command(ctx, "", "cat-file", "-t", commit)
	if err != nil || kind != "commit" {
		mu.Unlock()
		return nil, nil, fmt.Errorf("local config commit unavailable")
	}
	return s, mu.Unlock, nil
}

// LocalConfig reads configuration only from existing immutable local objects.
func (r *Repo) LocalConfig(ctx context.Context, repo forge.RepoRef, commit string) ([]byte, error) {
	s, unlock, err := r.localPolicySession(ctx, repo, commit)
	if err != nil {
		return nil, err
	}
	defer unlock()
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

// LocalGoverningFiles reads the complete policy tree without fetching or refs.
func (r *Repo) LocalGoverningFiles(ctx context.Context, repo forge.RepoRef, commit string) (forge.GoverningFiles, error) {
	s, unlock, err := r.localPolicySession(ctx, repo, commit)
	if err != nil {
		return forge.GoverningFiles{}, err
	}
	defer unlock()
	return s.governingFiles(ctx, forge.GoverningFilesRequest{CommitSHA: commit, AllAttributes: true, PathsComplete: true})
}
