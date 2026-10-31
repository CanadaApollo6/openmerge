package gitops

import (
	"context"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

// maxTreeDedupeListing bounds the workflow listing; larger is unknown.
const maxTreeDedupeListing = 1 << 20

// TreeDedupeInputs reads ADR 0015 shadow key inputs for an existing local
// commit with Git's own readers: `rev-parse <commit>^{tree}` and a recursive
// `ls-tree` of the workflow directories. Like LocalConfig it never fetches,
// resolves credentials, updates refs or waits for a busy cache; any failure
// is returned so the caller records the key as unknown.
func (r *Repo) TreeDedupeInputs(ctx context.Context, repo forge.RepoRef, commit string) (treededupe.GitInputs, error) {
	s, unlock, err := r.localPolicySession(ctx, repo, commit)
	if err != nil {
		return treededupe.GitInputs{}, err
	}
	defer unlock()
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
