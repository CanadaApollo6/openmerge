package gitops

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.GoverningFilesReader = (*Repo)(nil)

const emptyTreeObject = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// GoverningFiles uses the same hardened diffRead session as RevisionDiff.
// The contents API cannot provide bounded raw blobs, modes and tree-proven
// absence together; this reader pins all three to one verified commit.
func (r *Repo) GoverningFiles(ctx context.Context, repo forge.RepoRef, req forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	out := forge.GoverningFiles{CommitSHA: req.CommitSHA, Semantics: forge.DiffSemantics}
	fail := func(reason forge.DiffFailureReason, err error) (forge.GoverningFiles, error) {
		out.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{reason}}
		return out, err
	}
	if !forge.ValidDiffObjectID(req.CommitSHA) {
		return fail(forge.DiffInvalidObjectID, errors.New("governing read requires full commit object ID"))
	}
	s, unlock, err := r.openSession(ctx, repo, true)
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	defer unlock()
	if err := s.commits(ctx, req.CommitSHA); err != nil {
		reason := forge.DiffReadFailure
		if strings.Contains(err.Error(), "not our ref") || strings.Contains(err.Error(), "couldn't find remote ref") {
			reason = forge.DiffMissingObject
		}
		return fail(reason, err)
	}
	return s.governingFiles(ctx, req)
}

func (s *session) governingFiles(ctx context.Context, req forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	out := forge.GoverningFiles{CommitSHA: req.CommitSHA, Semantics: forge.DiffSemantics}
	fail := func(reason forge.DiffFailureReason, err error) (forge.GoverningFiles, error) {
		out.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{reason}}
		return out, err
	}
	kind, err := s.command(ctx, "", "cat-file", "-t", req.CommitSHA)
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	if kind != "commit" {
		return fail(forge.DiffInvalidObjectID, errors.New("governing object is not a commit"))
	}
	history, reason, err := s.diffHistory(ctx)
	if reason != "" {
		return fail(reason, err)
	}
	if req.AllAttributes {
		// Diff the immutable tree against Git's canonical empty tree with only
		// .gitattributes pathspecs. Unlike ls-tree, diff-tree accepts glob magic
		// here and emits no unrelated source-tree names. The descendant pathspec
		// also exposes a directory named .gitattributes for the regular-file
		// probe below.
		raw, overflow, err := s.diffCommand(ctx, forge.MaxDiffBytes, "diff-tree", "-r", "--name-only", "--no-commit-id", "-z", "--no-renames", emptyTreeObject, req.CommitSHA, "--", ".gitattributes", ":(glob)**/.gitattributes", ":(glob)**/.gitattributes/**")
		if err != nil {
			return fail(forge.DiffReadFailure, err)
		}
		if overflow {
			return fail(forge.DiffByteLimit, nil)
		}
		if raw != "" && !strings.HasSuffix(raw, "\x00") {
			return fail(forge.DiffReadFailure, errors.New("incomplete governing tree names"))
		}
		req.Paths = nil
		seen := map[string]bool{}
		for _, name := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
			for candidate := name; candidate != "." && candidate != ""; candidate = path.Dir(candidate) {
				if path.Base(candidate) == ".gitattributes" && !seen[candidate] {
					seen[candidate] = true
					req.Paths = append(req.Paths, candidate)
					if len(req.Paths) > forge.MaxGoverningFiles {
						return fail(forge.DiffFileLimit, nil)
					}
				}
			}
		}
		slices.Sort(req.Paths)
		req.PathsComplete = true
	}
	out, err = forge.CollectGoverningFiles(ctx, req, func(ctx context.Context, p string, budget int) (forge.GoverningFile, error) {
		f := forge.GoverningFile{Path: p, Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}}
		raw, overflow, err := s.diffCommand(ctx, forge.MaxDiffBytes, "ls-tree", "-z", "--full-tree", req.CommitSHA, "--", ":(literal)"+p)
		if ctx.Err() != nil {
			return f, ctx.Err()
		}
		if overflow {
			f.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{forge.DiffByteLimit}}
			return f, nil
		}
		if err != nil {
			return f, err
		}
		if raw == "" {
			return f, nil
		}
		header, name, ok := strings.Cut(strings.TrimSuffix(raw, "\x00"), "\t")
		fields := strings.Fields(header)
		if !ok || name != p || len(fields) != 3 || !forge.ValidDiffObjectID(fields[2]) {
			return f, errors.New("invalid governing tree entry")
		}
		f.Present, f.Mode, f.ObjectID = true, fields[0], fields[2]
		incomplete := func(reason forge.DiffFailureReason) (forge.GoverningFile, error) {
			f.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{reason}}
			return f, nil
		}
		if fields[1] != "blob" || f.Mode != "100644" && f.Mode != "100755" {
			return incomplete(forge.DiffSpecialMode)
		}
		sizeText, err := s.command(ctx, "", "cat-file", "-s", f.ObjectID)
		if err != nil {
			return f, err
		}
		var size int64
		if _, err := fmt.Sscan(sizeText, &size); err != nil || size < 0 {
			return f, errors.New("invalid governing blob size")
		}
		if size > int64(budget) {
			return incomplete(forge.DiffByteLimit)
		}
		content, overflow, err := s.diffCommand(ctx, budget, "cat-file", "blob", f.ObjectID)
		if ctx.Err() != nil {
			return f, ctx.Err()
		}
		if overflow {
			return incomplete(forge.DiffByteLimit)
		}
		if err != nil {
			return f, err
		}
		f.Content = content
		return f, nil
	})
	current, reason, historyErr := s.diffHistory(ctx)
	if reason != "" {
		return fail(reason, historyErr)
	}
	if !history.same(current) {
		return fail(forge.DiffShallowHistory, nil)
	}
	if ctx.Err() != nil {
		return fail(forge.DiffReadFailure, ctx.Err())
	}
	return out, err
}
