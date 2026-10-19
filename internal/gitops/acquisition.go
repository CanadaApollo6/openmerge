package gitops

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
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
	lease := fmt.Sprintf("%x", nonce)
	state, _ := acquisitionStates.Load(s.dir)
	live := state.(map[string]bool)
	live[lease] = true
	refs := make([]string, 0, len(shas))
	cleanup := func() {
		delete(live, lease)
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
		ref := fmt.Sprintf("refs/openmerge-cache/acquired/%s/%d", lease, i)
		refs = append(refs, ref)
		if _, err := s.command(ctx, "", "update-ref", ref, sha); err != nil {
			cleanup()
			return nil, err
		}
	}
	return func() {
		// All lease state and refs share the readers/maintenance cache lock.
		lock, _ := cacheLocks.LoadOrStore(s.dir, &sync.Mutex{})
		mu := lock.(*sync.Mutex)
		mu.Lock()
		defer mu.Unlock()
		// Repeated releases retry deletion; maintenance also retries non-live refs.
		cleanup()
	}, nil
}

// acquisitionStates is process-wide, like cacheLocks. Each map is accessed only
// under that directory's cache lock. Presence means startup cleanup succeeded.
var acquisitionStates sync.Map // absolute bare repository path -> map[nonce]live

const acquiredRefPrefix = "refs/openmerge-cache/acquired/"

// startAcquisitions runs on the first acquiring session per cache in this
// process. The cache is process-exclusive, so no inherited lease can be live.
// Failed startup cleanup is retried before any new acquisition is permitted.
func (s *session) startAcquisitions(ctx context.Context) error {
	if _, ok := acquisitionStates.Load(s.dir); ok {
		return nil
	}
	if err := s.sweepAcquisitions(ctx, nil); err != nil {
		return err
	}
	acquisitionStates.Store(s.dir, make(map[string]bool))
	return nil
}

func (s *session) sweepAcquisitions(ctx context.Context, live map[string]bool) error {
	out, err := s.command(ctx, "", "for-each-ref", "--format=%(refname)", acquiredRefPrefix)
	if err != nil {
		return err
	}
	var refs []string
	for _, ref := range strings.Fields(out) {
		nonce, _, _ := strings.Cut(strings.TrimPrefix(ref, acquiredRefPrefix), "/")
		if !live[nonce] {
			refs = append(refs, ref)
		}
	}
	return s.deleteRefs(ctx, refs)
}

// Keep the existing mutex shared with cache-only readers while allowing an
// acquisition deadline to expire during contention, without a waiter goroutine.
func lockCache(ctx context.Context, mu *sync.Mutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
