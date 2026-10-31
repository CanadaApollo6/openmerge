package gitops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

// maxTreeDedupeListing bounds the workflow listing; larger is unknown.
const maxTreeDedupeListing = 1 << 20

// TreeDedupeInputs reads ADR 0015 shadow key inputs for an existing local
// commit with Git's own readers: `rev-parse <commit>^{tree}` and a recursive
// `ls-tree` of the workflow directories. It never fetches, resolves
// credentials, updates refs, touches an index or worktree, or runs
// maintenance, and it never takes the cache mutex: landing and every other
// cache writer must never wait on shadow work. Object reads in a bare cache
// are safe beside concurrent fetch, push and gc (Git rescans packs on a
// miss); a read that still loses a race fails and the caller records the key
// as unknown.
func (r *Repo) TreeDedupeInputs(ctx context.Context, repo forge.RepoRef, commit string) (treededupe.GitInputs, error) {
	s, err := r.objectReadSession(ctx, repo, commit)
	if err != nil {
		return treededupe.GitInputs{}, err
	}
	tree, err := s.command(ctx, "", "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return treededupe.GitInputs{}, err
	}
	if !forge.ValidDiffObjectID(tree) || strings.ToLower(tree) != tree {
		return treededupe.GitInputs{}, errors.New("tree dedupe: invalid tree object ID")
	}
	args := []string{"ls-tree", "-r", "-z", "--full-tree", commit, "--"}
	for _, dir := range treededupe.WorkflowDirs {
		args = append(args, ":(literal)"+dir)
	}
	raw, overflow, err := s.diffCommand(ctx, maxTreeDedupeListing, args...)
	if err != nil {
		return treededupe.GitInputs{}, err
	}
	if overflow || raw != "" && !strings.HasSuffix(raw, "\x00") {
		return treededupe.GitInputs{}, errors.New("tree dedupe: incomplete workflow listing")
	}
	out := treededupe.GitInputs{Tree: tree}
	if raw == "" {
		return out, nil
	}
	// ls-tree -z emits "<mode> SP <type> SP <object> TAB <path> NUL" records.
	for record := range strings.SplitSeq(strings.TrimSuffix(raw, "\x00"), "\x00") {
		header, path, ok := strings.Cut(record, "\t")
		fields := strings.Split(header, " ")
		if !ok || len(fields) != 3 || path == "" {
			return treededupe.GitInputs{}, errors.New("tree dedupe: invalid workflow listing")
		}
		out.Workflows = append(out.Workflows, treededupe.Workflow{Mode: fields[0], Type: fields[1], OID: fields[2], Path: path})
	}
	return out, nil
}

// objectReadSession opens an existing local cache for immutable object reads
// WITHOUT the cache mutex. Only read-only object plumbing may run through it.
func (r *Repo) objectReadSession(ctx context.Context, repo forge.RepoRef, commit string) (*session, error) {
	if !forge.ValidDiffObjectID(commit) {
		return nil, errors.New("tree dedupe requires full commit ID")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Join(r.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(repo.Path))))
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		return nil, err
	}
	s := &session{r: r, dir: dir, diffRead: true}
	if kind, err := s.command(ctx, "", "cat-file", "-t", commit); err != nil || kind != "commit" {
		return nil, errors.New("tree dedupe commit unavailable")
	}
	return s, nil
}
