package gitops

import (
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ReadCommitMessage reads an immutable tested object (including fast-forward
// markers). Activation supplies pinned tested member SHAs, never PR body text
// or a moving branch. It performs no push and does not rewrite any message.
func (r *Repo) ReadCommitMessage(ctx context.Context, repo forge.RepoRef, sha string) (string, error) {
	if !forge.ValidDiffObjectID(sha) {
		return "", fmt.Errorf("invalid tested commit SHA")
	}
	s, closeSession, err := r.openSession(ctx, repo, false)
	if err != nil {
		return "", err
	}
	defer closeSession()
	if err := s.fetch(ctx, sha); err != nil {
		return "", err
	}
	return s.command(ctx, "", "show", "-s", "--format=%B", sha)
}
