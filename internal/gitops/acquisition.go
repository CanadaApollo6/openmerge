package gitops

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.CommitAcquirer = (*Repo)(nil)

// AcquireCommits hydrates exact object IDs, never branch tips. Private lease
// refs keep the objects reachable across maintenance in intervening operations
// (notably rebase source-history reads). A fresh landing reacquires after a
// restart; it never depends on a previous process's cache or lease.
func (r *Repo) AcquireCommits(ctx context.Context, repo forge.RepoRef, shas []string) (func(), error) {
	if len(shas) == 0 || len(shas) > forge.MaxFirstParentCommits+2 {
		return nil, errors.New("invalid acquisition commit count")
	}
	for _, sha := range shas {
		if !forge.ValidDiffObjectID(sha) {
			return nil, errors.New("acquisition requires full commit IDs")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	s, unlock, err := r.openSession(ctx, repo, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(shas))
	cleanup := func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = s.deleteRefs(cleanupCtx, refs)
	}
	for i, sha := range shas {
		if err := s.commits(ctx, sha); err != nil {
			cleanup()
			return nil, err
		}
		kind, err := s.command(ctx, "", "cat-file", "-t", sha)
		if err != nil || kind != "commit" {
			cleanup()
			return nil, errors.New("acquired object is not a commit")
		}
		// update-ref requires the exact acquired object; no remote ref resolution.
		ref := fmt.Sprintf("refs/openmerge-cache/acquired/%x/%d", nonce, i)
		refs = append(refs, ref)
		if _, err := s.command(ctx, "", "update-ref", ref, sha); err != nil {
			cleanup()
			return nil, err
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			// Use the same lock as readers and maintenance without resolving a remote.
			lock, _ := cacheLocks.Load(s.dir)
			mu := lock.(*sync.Mutex)
			mu.Lock()
			defer mu.Unlock()
			cleanup()
		})
	}, nil
}
