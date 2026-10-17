package fake

import (
	"context"
	"errors"
	"maps"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// VerifyFastForward validates marker shape and ancestry without changing refs.
func (f *Forge) VerifyFastForward(ctx context.Context, repo forge.RepoRef, base, tested, head string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "VerifyFastForward"); err != nil {
		return err
	}
	return f.repo(repo).verifyFastForward(base, tested, head)
}

func (r *repository) verifyFastForward(base, tested, head string) error {
	marker, ok := r.commits[tested]
	_, headOK := r.commits[head]
	if !ok || !headOK || len(marker.Parents) != 1 || marker.Parents[0] != head || !maps.Equal(r.files(tested), r.files(head)) {
		return errors.New("fast-forward marker must have only the pinned head as parent and the same tree")
	}
	if base == head || !r.isAncestor(base, head) {
		return forge.ErrNotFastForward
	}
	return nil
}
