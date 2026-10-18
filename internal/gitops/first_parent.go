package gitops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.FirstParentCommitReader = (*Repo)(nil)

// FirstParentCommits reads the immutable first-parent chain from head through
// but excluding base. It never resolves a remote, fetches, or follows a
// replacement/graft: tested-member selection must use only already acquired
// objects. Results are newest first and a missing first-parent base fails
// closed rather than falling back to another graph edge.
func (r *Repo) FirstParentCommits(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	if limit < 1 || limit > 10000 {
		return nil, false, errors.New("invalid first-parent history limit")
	}
	for _, sha := range []string{base, head} {
		if !forge.ValidDiffObjectID(sha) {
			return nil, false, fmt.Errorf("invalid first-parent commit SHA")
		}
	}
	dir := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path))))
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	s := &session{r: r, dir: dir, diffRead: true}
	for _, sha := range []string{base, head} {
		if _, _, err := s.run(ctx, "", "cat-file", "-e", sha+"^{commit}"); err != nil {
			return nil, false, fmt.Errorf("local first-parent commit unavailable: %w", err)
		}
	}
	out, err := s.command(ctx, "", "rev-list", "--first-parent", "--max-count="+strconv.Itoa(limit+1), head)
	if err != nil {
		return nil, false, err
	}
	chain := strings.Fields(out)
	for i, sha := range chain {
		if sha == base {
			return chain[:i], false, nil
		}
	}
	if len(chain) > limit {
		return chain[:limit], true, nil
	}
	return nil, false, fmt.Errorf("base %q is not on the first-parent history of %q", base, head)
}
