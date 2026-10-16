package gitops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func (s *session) heads(ctx context.Context, prs []forge.BuildPR) (err error) {
	if len(prs) == 0 {
		return nil
	}
	var specs []string
	var temporaryRefs, cachedRefs []string
	var missing []forge.BuildPR
	cached := make(map[string]string)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, s.deleteRefs(cleanupCtx, temporaryRefs))
	}()
	for _, pr := range prs {
		if pr.Number <= 0 || !validSHA(pr.HeadSHA) {
			return errors.New("PR number and full pinned head SHA are required")
		}
		ref := s.r.cfg.HeadRef(pr.Number)
		if !strings.HasPrefix(ref, "refs/") {
			return errors.New("HeadRef must return a fully qualified git ref")
		}
		if _, err := s.command(ctx, "", "check-ref-format", ref); err != nil {
			return err
		}
		if _, _, err := s.run(ctx, "", "cat-file", "-e", pr.HeadSHA+"^{commit}"); err == nil {
			cachedRefs = append(cachedRefs, ref)
			cached[ref] = pr.HeadSHA
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		temporary := fmt.Sprintf("refs/openmerge-cache/pr/%d", pr.Number)
		temporaryRefs = append(temporaryRefs, temporary)
		missing = append(missing, pr)
		specs = append(specs, "+"+ref+":"+temporary)
	}
	if len(cachedRefs) > 0 {
		values, err := s.read(ctx, cachedRefs...)
		if err != nil {
			return err
		}
		for ref, expected := range cached {
			if values[ref] != expected {
				return fmt.Errorf("PR head %s no longer has its pinned head", ref)
			}
		}
	}
	if len(specs) > 0 {
		if err := s.fetch(ctx, specs...); err != nil {
			return err
		}
	}
	for _, pr := range missing {
		actual, err := s.command(ctx, "", "rev-parse", fmt.Sprintf("refs/openmerge-cache/pr/%d^{commit}", pr.Number))
		if err != nil {
			return err
		}
		if actual != pr.HeadSHA {
			return fmt.Errorf("PR %d no longer has its pinned head", pr.Number)
		}
	}
	return nil
}

func (s *session) commit(ctx context.Context, tree, message string, parents ...string) (string, error) {
	args := []string{"commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return s.command(ctx, message+"\n", args...)
}

func (s *session) mergeTree(ctx context.Context, base, ours, theirs string) (string, error) {
	args := []string{"merge-tree", "--write-tree"}
	if base != "" {
		if s.r.explicitBase {
			args = append(args, "--merge-base="+base)
		} else {
			// Git 2.38/2.39 do not expose --merge-base. A synthetic commit
			// with our tree and only this parent forces that same merge base.
			tree, err := s.command(ctx, "", "rev-parse", ours+"^{tree}")
			if err != nil {
				return "", err
			}
			ours, err = s.commit(ctx, tree, "openmerge replay base", base)
			if err != nil {
				return "", err
			}
		}
	}
	out, diagnostic, err := s.run(ctx, "", append(args, ours, theirs)...)
	if exitCode(err, 1) {
		return "", forge.ErrConflict
	}
	if err != nil {
		return "", s.failure("merge-tree", diagnostic, err)
	}
	tree, _, _ := strings.Cut(out, "\n")
	if !validSHA(tree) {
		return "", errors.New("git merge-tree returned an invalid tree SHA")
	}
	return tree, nil
}

// BuildTestRef composes commits without an index or worktree, then force-pushes
// the OpenMerge-owned test ref. All pinned heads are verified before composing.
func (r *Repo) BuildTestRef(ctx context.Context, repo forge.RepoRef, req forge.BuildRequest) (string, error) {
	if req.Ref == "" || !forge.ValidBatchID(req.BatchID) || len(req.PRs) == 0 {
		return "", errors.New("test ref, valid batch ID, and PRs are required")
	}
	if req.Method != forge.Merge && req.Method != forge.Squash && req.Method != forge.Rebase && req.Method != forge.FastForward {
		return "", errors.New("unsupported git merge method")
	}
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return "", err
	}
	defer unlock()
	ref, err := s.branch(ctx, req.Ref)
	if err != nil {
		return "", err
	}
	if err := s.heads(ctx, req.PRs); err != nil {
		return "", err
	}
	if err := s.commits(ctx, req.BaseSHA); err != nil {
		return "", err
	}
	top := req.BaseSHA
	for i, pr := range req.PRs {
		if req.Method == forge.FastForward {
			_, diagnostic, err := s.run(ctx, "", "merge-base", "--is-ancestor", top, pr.HeadSHA)
			if top == pr.HeadSHA || exitCode(err, 1) {
				return "", fmt.Errorf("PR #%d: %w", pr.Number, forge.ErrNotFastForward)
			}
			if err != nil {
				return "", s.failure("merge-base", diagnostic, err)
			}
			top = pr.HeadSHA
			continue
		}
		metadata, author, err := s.metadata(ctx, repo, pr)
		if err != nil {
			return "", err
		}
		if req.Method == forge.Rebase {
			merges, err := s.command(ctx, "", "rev-list", "--merges", "--max-count=1", pr.HeadSHA, "^"+req.BaseSHA)
			if err != nil {
				return "", err
			}
			if merges != "" {
				return "", fmt.Errorf("rebase cannot replay merge commits in PR #%d; use the merge or squash method: %w", pr.Number, forge.ErrConflict)
			}
			originals, err := s.command(ctx, "", "rev-list", "--reverse", "--topo-order", pr.HeadSHA, "^"+req.BaseSHA)
			if err != nil {
				return "", err
			}
			commits := strings.Fields(originals)
			if len(commits) == 0 {
				return "", fmt.Errorf("PR %d has no commits to replay", pr.Number)
			}
			for j, original := range commits {
				tree, err := s.mergeTree(ctx, original+"^", top, original)
				if err != nil {
					return "", err
				}
				originalAuthor, date, message, err := s.original(ctx, original)
				if err != nil {
					return "", err
				}
				subject, _, _ := strings.Cut(message, "\n")
				message = stripMetadataLines(message, "OpenMerge-Batch")
				if stripMetadataLines(subject, "OpenMerge-Batch") == "" {
					message = fmt.Sprintf("PR %d\n\n%s", pr.Number, strings.TrimSpace(message))
				}
				// Replaying must create a new identity even if author, clock,
				// tree and first parent happen to match the original commit.
				trailers := []string{"OpenMerge-Replayed-From: " + original}
				if i == len(req.PRs)-1 && j == len(commits)-1 {
					trailers = append(trailers, "OpenMerge-Batch: "+req.BatchID)
				}
				message, err = s.addTrailers(ctx, message, trailers...)
				if err != nil {
					return "", err
				}
				top, err = s.commitAs(ctx, tree, message, originalAuthor, date, top)
				if err != nil {
					return "", err
				}
			}
			continue
		}
		tree, err := s.mergeTree(ctx, "", top, pr.HeadSHA)
		if err != nil {
			return "", err
		}
		message, err := s.prMessage(ctx, repo, req, metadata, author, i == len(req.PRs)-1)
		if err != nil {
			return "", err
		}
		parents := []string{top}
		if req.Method == forge.Merge {
			parents = append(parents, pr.HeadSHA)
		}
		top, err = s.commitAs(ctx, tree, message, author, "", parents...)
		if err != nil {
			return "", err
		}
	}
	if err := s.resolveRemote(ctx, repo); err != nil {
		return "", err
	}
	if _, err := s.command(ctx, "", "push", "--porcelain", "--force", "--", s.remote, top+":"+ref); err != nil {
		return "", err
	}
	return top, nil
}
