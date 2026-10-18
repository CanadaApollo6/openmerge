package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ReadCommitMessage reads an immutable tested object (including fast-forward
// markers) from the local cache only. Acquisition is a separate operation.
// Activation supplies pinned tested member SHAs, never PR body text
// or a moving branch. It performs no push and does not rewrite any message.
func (r *Repo) ReadCommitMessage(ctx context.Context, repo forge.RepoRef, sha string) (string, error) {
	if !forge.ValidDiffObjectID(sha) {
		return "", fmt.Errorf("invalid tested commit SHA")
	}
	// This read must remain independent of remote resolution and acquisition.
	// diffRead disables replacements, grafts, lazy fetching and all transports.
	dir := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path))))
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	s := &session{r: r, dir: dir, diffRead: true}
	return s.command(ctx, "", "show", "-s", "--format=%B", sha+"^{commit}")
}
