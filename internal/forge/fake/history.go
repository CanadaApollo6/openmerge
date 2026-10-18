package fake

import (
	"context"
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CommitsBetween enumerates a deterministic reverse topological graph difference.
func (f *Forge) CommitsBetween(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CommitsBetween"); err != nil {
		return nil, false, err
	}
	r := f.repo(repo)
	if limit < 1 {
		return nil, false, fmt.Errorf("invalid history limit")
	}
	for _, sha := range []string{base, head} {
		if _, ok := r.commits[sha]; !ok {
			return nil, false, fmt.Errorf("unknown commit %q", sha)
		}
	}
	// Postorder visits parents before children; reversing lists children first.
	var visit func(string, map[string]bool, *[]string)
	visit = func(sha string, seen map[string]bool, order *[]string) {
		if seen[sha] {
			return
		}
		seen[sha] = true
		for _, p := range r.commits[sha].Parents {
			visit(p, seen, order)
		}
		*order = append(*order, sha)
	}
	excluded := make(map[string]bool)
	var ignored, commits []string
	visit(base, excluded, &ignored)
	visit(head, excluded, &commits)
	slices.Reverse(commits)
	return commits[:min(limit, len(commits))], len(commits) > limit, nil
}

// ReadCommitMessage implements the immutable tested-object reader used by the
// shadow gate. It performs no remote reads or synthetic metadata generation.
func (f *Forge) ReadCommitMessage(ctx context.Context, repo forge.RepoRef, sha string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !forge.ValidDiffObjectID(sha) {
		return "", fmt.Errorf("invalid commit ID")
	}
	commit, err := f.Commit(repo, sha)
	if err != nil {
		return "", err
	}
	return commit.Message, nil
}
