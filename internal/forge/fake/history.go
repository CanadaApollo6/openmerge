package fake

import (
	"context"
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CommitsBetween enumerates a deterministic reverse topological graph difference.
func (f *Forge) CommitsBetween(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	if limit < 1 || limit > forge.MaxCommitHistoryCommits {
		return nil, false, fmt.Errorf("invalid commit history limit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "CommitsBetween"); err != nil {
		return nil, false, err
	}
	r := f.repo(repo)
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

// FirstParentCommits reads the queue-generated composition chain without
// traversing contributor merge parents. It mirrors the immutable, bounded
// cache reader used by the CLI-backed forge.
func (f *Forge) FirstParentCommits(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	if limit < 1 || limit > forge.MaxFirstParentCommits {
		return nil, false, fmt.Errorf("invalid first-parent history limit")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failure(ctx, "FirstParentCommits"); err != nil {
		return nil, false, err
	}
	r := f.repo(repo)
	for _, sha := range []string{base, head} {
		if _, ok := r.commits[sha]; !ok {
			return nil, false, fmt.Errorf("unknown commit %q", sha)
		}
	}
	commits := make([]string, 0, limit)
	for current := head; current != base; {
		if len(commits) == limit {
			return commits, true, nil
		}
		commit := r.commits[current]
		if len(commit.Parents) == 0 {
			return nil, false, fmt.Errorf("base %q is not on the first-parent history of %q", base, head)
		}
		commits = append(commits, current)
		current = commit.Parents[0]
	}
	return commits, false, nil
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

// AcquireCommits validates immutable IDs. The fake's single durable object map
// has no separate cache or GC; real-git gate integration covers that boundary.
func (f *Forge) AcquireCommits(ctx context.Context, repo forge.RepoRef, shas []string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, sha := range shas {
		if !forge.ValidDiffObjectID(sha) {
			return nil, fmt.Errorf("acquisition requires full commit IDs")
		}
		if _, err := f.Commit(repo, sha); err != nil {
			return nil, err
		}
	}
	return func() {}, nil
}
