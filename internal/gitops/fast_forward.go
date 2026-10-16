package gitops

import (
	"context"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// VerifyFastForward validates the immutable marker and pinned landing history.
func (r *Repo) VerifyFastForward(ctx context.Context, repo forge.RepoRef, base, tested, head string) error {
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return err
	}
	defer unlock()
	return s.verifyFastForward(ctx, base, tested, head)
}

func (s *session) verifyFastForward(ctx context.Context, base, tested, head string) error {
	if err := s.commits(ctx, base, tested, head); err != nil {
		return err
	}
	shape, err := s.command(ctx, "", "show", "-s", "--format=%P%n%T", tested)
	if err != nil {
		return err
	}
	parents, tree, _ := strings.Cut(shape, "\n")
	headTree, err := s.command(ctx, "", "rev-parse", head+"^{tree}")
	if err != nil {
		return err
	}
	if parents != head || tree != headTree {
		return errors.New("fast-forward marker must have only the pinned head as parent and the same tree")
	}
	_, diagnostic, err := s.run(ctx, "", "merge-base", "--is-ancestor", base, head)
	if base == head || exitCode(err, 1) {
		return forge.ErrNotFastForward
	}
	if err != nil {
		return s.failure("merge-base", diagnostic, err)
	}
	return nil
}
