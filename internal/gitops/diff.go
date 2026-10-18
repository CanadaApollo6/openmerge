package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.RevisionDiffReader = (*Repo)(nil)
var errDiffOutputLimit = errors.New("diff output limit")

// limitedDiffBuffer bounds memory even when git produces a huge inventory/blob
// or diagnostic. exec closes the output pipe when Write refuses further bytes.
type limitedDiffBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedDiffBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		n, _ := b.Buffer.Write(p[:b.limit-b.Len()])
		b.overflow = true
		return n, errDiffOutputLimit
	}
	return b.Buffer.Write(p)
}

func (s *session) diffCommand(ctx context.Context, limit int, args ...string) (string, bool, error) {
	out := &limitedDiffBuffer{limit: limit}
	diagnostic := &limitedDiffBuffer{limit: 8192}
	err := s.runTo(ctx, "", out, diagnostic, args...)
	if err != nil {
		err = s.failure(args[0], diagnostic.String(), err)
	}
	return out.String(), out.overflow, err
}

// RevisionDiff uses the same cache lock, credential resolution, fetch and
// maintenance lifecycle as builds. It never writes a remote ref or cache ref.
func (r *Repo) RevisionDiff(ctx context.Context, repo forge.RepoRef, req forge.RevisionDiffRequest) (forge.RevisionDiff, error) {
	result := forge.NewRevisionDiff(req)
	fail := func(reason forge.DiffFailureReason, err error) (forge.RevisionDiff, error) {
		result.Inventory.Completeness = forge.DiffIncomplete
		result.Inventory.Reasons = append(result.Inventory.Reasons, reason)
		return result, err
	}
	if !forge.ValidDiffObjectID(req.BaseSHA) || !forge.ValidDiffObjectID(req.HeadSHA) {
		return fail(forge.DiffInvalidObjectID, errors.New("revision diff requires full commit object IDs"))
	}
	s, unlock, err := r.open(ctx, repo)
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	defer unlock()
	if err = s.commits(ctx, req.BaseSHA, req.HeadSHA); err != nil {
		reason := forge.DiffReadFailure
		if strings.Contains(err.Error(), "not our ref") || strings.Contains(err.Error(), "couldn't find remote ref") {
			reason = forge.DiffMissingObject
		}
		return fail(reason, err)
	}
	// Do not accept a tag object merely because ^{commit} peels it successfully.
	for _, id := range []string{req.BaseSHA, req.HeadSHA} {
		kind, err := s.command(ctx, "", "cat-file", "-t", id)
		if err != nil {
			return fail(forge.DiffReadFailure, err)
		}
		if kind != "commit" {
			return fail(forge.DiffInvalidObjectID, errors.New("revision diff endpoint is not a commit"))
		}
	}
	// Endpoint presence does not prove ancestry: ordinary fetches can leave an
	// existing shallow boundary in place. Check after fetching, under the cache
	// lock, on every read (including reads following cache maintenance).
	shallow, err := s.command(ctx, "", "rev-parse", "--is-shallow-repository")
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	if shallow == "true" {
		return fail(forge.DiffShallowHistory, nil)
	}
	if shallow != "false" {
		return fail(forge.DiffReadFailure, errors.New("invalid git shallow-repository response"))
	}
	bases, overflow, err := s.diffCommand(ctx, forge.MaxDiffBytes, "merge-base", "--all", req.BaseSHA, req.HeadSHA)
	if overflow {
		return fail(forge.DiffByteLimit, nil)
	}
	if exitCode(err, 1) {
		return fail(forge.DiffNoMergeBase, nil)
	}
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	ids := strings.Fields(bases)
	if len(ids) == 0 {
		return fail(forge.DiffNoMergeBase, nil)
	}
	if len(ids) != 1 {
		return fail(forge.DiffMultipleMergeBases, nil)
	}
	result.MergeBaseSHA = ids[0]
	// Raw, NUL-delimited output preserves tabs/newlines and arbitrary path bytes.
	// Disable rename heuristics and all attribute-dependent content transforms.
	raw, overflow, err := s.diffCommand(ctx, forge.MaxDiffBytes, "diff-tree", "-r", "--raw", "-z", "--no-abbrev", "--no-commit-id", "--no-renames", "--no-ext-diff", "--no-textconv", result.MergeBaseSHA, req.HeadSHA, "--")
	if overflow {
		return fail(forge.DiffByteLimit, nil)
	}
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	files, err := parseDiffRaw(raw)
	if err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	// A rename consumes at most two raw entries. Refuse pairing on an oversized
	// list: a discarded entry could make an apparently unique pairing ambiguous.
	if len(files) > 2*forge.MaxDiffFiles {
		result.Files = files[:forge.MaxDiffFiles]
		return fail(forge.DiffFileLimit, nil)
	}
	files = forge.PairExactDiffRenames(files)
	if len(files) > forge.MaxDiffFiles {
		result.Files = files[:forge.MaxDiffFiles]
		return fail(forge.DiffFileLimit, nil)
	}
	result.Files = files
	remaining := forge.MaxDiffBytes - len(raw)
	type binaryObservation struct{ binary bool }
	observed := map[string]binaryObservation{}
	for i := range result.Files {
		f := &result.Files[i]
		f.BinaryKnown = true
		if f.Rename == forge.DiffRenameUnpaired {
			result.Inventory.Reasons = appendReason(result.Inventory.Reasons, forge.DiffUnpairedRename)
		}
		for _, object := range []struct{ id, mode string }{{f.OldObjectID, f.OldMode}, {f.NewObjectID, f.NewMode}} {
			if object.id == "" || object.mode == "160000" {
				continue
			}
			if value, ok := observed[object.id]; ok {
				f.Binary = f.Binary || value.binary
				continue
			}
			sizeText, err := s.command(ctx, "", "cat-file", "-s", object.id)
			if err != nil {
				f.BinaryKnown = false
				return fail(forge.DiffReadFailure, err)
			}
			var size int64
			if _, err = fmt.Sscan(sizeText, &size); err != nil || size < 0 {
				f.BinaryKnown = false
				return fail(forge.DiffReadFailure, errors.New("invalid git blob size"))
			}
			if size > int64(remaining) {
				f.BinaryKnown = false
				return fail(forge.DiffByteLimit, nil)
			}
			blob, overflow, err := s.diffCommand(ctx, remaining, "cat-file", "blob", object.id)
			if overflow {
				f.BinaryKnown = false
				return fail(forge.DiffByteLimit, nil)
			}
			if err != nil {
				f.BinaryKnown = false
				return fail(forge.DiffReadFailure, err)
			}
			remaining -= len(blob)
			binary := strings.ContainsRune(blob, '\x00')
			observed[object.id] = binaryObservation{binary: binary}
			f.Binary = f.Binary || binary
		}
	}
	if len(result.Inventory.Reasons) == 0 {
		result.Inventory.Completeness = forge.DiffComplete
	}
	return result, nil
}

func appendReason(reasons []forge.DiffFailureReason, reason forge.DiffFailureReason) []forge.DiffFailureReason {
	for _, r := range reasons {
		if r == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func parseDiffRaw(raw string) ([]forge.DiffFile, error) {
	files := []forge.DiffFile{}
	for raw != "" {
		header, rest, ok := strings.Cut(raw, "\x00")
		if !ok {
			return nil, errors.New("unterminated git diff header")
		}
		path, rest, ok := strings.Cut(rest, "\x00")
		fields := strings.Fields(strings.TrimPrefix(header, ":"))
		if !ok || path == "" || !strings.HasPrefix(header, ":") || len(fields) != 5 {
			return nil, errors.New("invalid git raw diff")
		}
		f := forge.DiffFile{OldPath: path, NewPath: path, OldMode: fields[0], NewMode: fields[1], OldObjectID: fields[2], NewObjectID: fields[3], Rename: forge.DiffNotRenamed}
		switch fields[4] {
		case "A":
			f.OldPath, f.OldMode, f.OldObjectID = "", "", ""
		case "D":
			f.NewPath, f.NewMode, f.NewObjectID = "", "", ""
		case "M", "T":
		default:
			return nil, errors.New("unsupported git raw diff status")
		}
		f.Submodule = f.OldMode == "160000" || f.NewMode == "160000"
		files = append(files, f)
		raw = rest
	}
	return files, nil
}
