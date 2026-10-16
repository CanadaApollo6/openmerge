package fake

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// SetDefaultBranch changes repository metadata independently of branch tips.
func (f *Forge) SetDefaultBranch(repo forge.RepoRef, branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(repo).defaultBranch = branch
}

// DefaultBranch reports the first initialized base unless explicitly changed.
func (f *Forge) DefaultBranch(ctx context.Context, repo forge.RepoRef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "DefaultBranch"); err != nil {
		return "", err
	}
	branch := f.repo(repo).defaultBranch
	if branch == "" {
		return "", errors.New("repository default branch is missing")
	}
	return branch, nil
}
