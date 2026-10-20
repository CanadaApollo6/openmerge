package gitops

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// CommitsBetween reads the complete graph, bounding output with one lookahead.
func (r *Repo) CommitsBetween(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	if err := forge.ValidateHistoryLimit(limit, forge.MaxCommitHistoryCommits); err != nil {
		return nil, false, err
	}
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	if err := s.commits(ctx, base, head); err != nil {
		return nil, false, err
	}
	shallow, err := s.command(ctx, "", "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, false, err
	}
	if shallow != "false" {
		return nil, false, errors.New("incomplete shallow history")
	}
	out, err := s.command(ctx, "", "rev-list", "--topo-order", "--max-count="+strconv.Itoa(limit+1), head, "^"+base, "--")
	if err != nil {
		return nil, false, err
	}
	commits := strings.Fields(out)
	return commits[:min(limit, len(commits))], len(commits) > limit, nil
}
