package fake

import (
	"context"
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// SetLinearHistoryRequired changes the base's observed history policy.
func (f *Forge) SetLinearHistoryRequired(repo forge.RepoRef, base string, required bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repo(repo)
	if r.linearHistory == nil {
		r.linearHistory = make(map[string]bool)
	}
	r.linearHistory[base] = required
}

// LinearHistoryRequired reads policy; FailNext can model unknown requirements.
func (f *Forge) LinearHistoryRequired(ctx context.Context, repo forge.RepoRef, base string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "LinearHistoryRequired"); err != nil {
		return false, err
	}
	return f.repo(repo).linearHistory[base], nil
}

// HasMergeCommits walks every parent edge, excluding history already on base.
func (f *Forge) HasMergeCommits(ctx context.Context, repo forge.RepoRef, base, head string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "HasMergeCommits"); err != nil {
		return false, err
	}
	r := f.repo(repo)
	for _, sha := range []string{base, head} {
		if _, ok := r.commits[sha]; !ok {
			return false, fmt.Errorf("unknown commit %q", sha)
		}
	}
	for sha, commit := range r.commits {
		if len(commit.Parents) > 1 && r.isAncestor(sha, head) && !r.isAncestor(sha, base) {
			return true, nil
		}
	}
	return false, nil
}
