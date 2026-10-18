package fake

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.RevisionDiffReader = (*Forge)(nil)

// RevisionDiff observes the fake's immutable snapshots, including compact-mode
// snapshots. Fake files are regular 100644 blobs; mode/gitlink cases use local
// git contract fixtures. This method adds no state to legacy simulations.
func (f *Forge) RevisionDiff(ctx context.Context, repo forge.RepoRef, req forge.RevisionDiffRequest) (forge.RevisionDiff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := forge.NewRevisionDiff(req)
	fail := func(reason forge.DiffFailureReason, err error) (forge.RevisionDiff, error) {
		result.Inventory.Completeness = forge.DiffIncomplete
		result.Inventory.Reasons = append(result.Inventory.Reasons, reason)
		if len(result.Files) > forge.MaxDiffFiles {
			result.Files = result.Files[:forge.MaxDiffFiles]
		}
		return result, err
	}
	if err := f.failure(ctx, "RevisionDiff"); err != nil {
		return fail(forge.DiffReadFailure, err)
	}
	if !forge.ValidDiffObjectID(req.BaseSHA) || !forge.ValidDiffObjectID(req.HeadSHA) {
		return fail(forge.DiffInvalidObjectID, errors.New("revision diff requires full commit object IDs"))
	}
	r := f.repo(repo)
	for _, id := range []string{req.BaseSHA, req.HeadSHA} {
		if _, ok := r.commits[id]; !ok {
			return fail(forge.DiffMissingObject, fmt.Errorf("revision diff commit %s is missing", id))
		}
	}
	ancestors := func(start string) map[string]bool {
		result := map[string]bool{}
		pending := []string{start}
		for len(pending) > 0 {
			sha := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if result[sha] {
				continue
			}
			result[sha] = true
			pending = append(pending, r.commits[sha].Parents...)
		}
		return result
	}
	common := []string{}
	headAncestors := ancestors(req.HeadSHA)
	for sha := range ancestors(req.BaseSHA) {
		if headAncestors[sha] {
			common = append(common, sha)
		}
	}
	bases := []string{}
	for _, a := range common {
		best := true
		for _, b := range common {
			if a != b && r.isAncestor(a, b) {
				best = false
				break
			}
		}
		if best {
			bases = append(bases, a)
		}
	}
	if len(bases) == 0 {
		return fail(forge.DiffNoMergeBase, nil)
	}
	if len(bases) != 1 {
		return fail(forge.DiffMultipleMergeBases, nil)
	}
	result.MergeBaseSHA = bases[0]
	old, newFiles := r.files(bases[0]), r.files(req.HeadSHA)
	paths := []string{}
	for p := range old {
		paths = append(paths, p)
	}
	for p := range newFiles {
		if _, ok := old[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	blobs := map[string]string{}
	rawBytes := 0
	for _, path := range paths {
		a, aok := old[path]
		b, bok := newFiles[path]
		if aok && bok && a == b {
			continue
		}
		file := forge.DiffFile{Rename: forge.DiffNotRenamed}
		if aok {
			file.OldPath, file.OldMode, file.OldObjectID = path, "100644", diffBlobID(a)
			blobs[file.OldObjectID] = a
		}
		if bok {
			file.NewPath, file.NewMode, file.NewObjectID = path, "100644", diffBlobID(b)
			blobs[file.NewObjectID] = b
		}
		rawBytes += len(path) + 148 // sha256 raw record, including NUL delimiters
		if rawBytes > forge.MaxDiffBytes {
			return fail(forge.DiffByteLimit, nil)
		}
		result.Files = append(result.Files, file)
		if len(result.Files) > 2*forge.MaxDiffFiles {
			result.Files = result.Files[:forge.MaxDiffFiles]
			return fail(forge.DiffFileLimit, nil)
		}
	}
	result.Files = forge.PairExactDiffRenames(result.Files)
	if len(result.Files) > forge.MaxDiffFiles {
		result.Files = result.Files[:forge.MaxDiffFiles]
		return fail(forge.DiffFileLimit, nil)
	}
	remaining := forge.MaxDiffBytes - rawBytes
	seen := map[string]bool{}
	for i := range result.Files {
		file := &result.Files[i]
		file.BinaryKnown = true
		if file.Rename == forge.DiffRenameUnpaired && !slices.Contains(result.Inventory.Reasons, forge.DiffUnpairedRename) {
			result.Inventory.Reasons = append(result.Inventory.Reasons, forge.DiffUnpairedRename)
		}
		for _, id := range []string{file.OldObjectID, file.NewObjectID} {
			if id == "" {
				continue
			}
			if !seen[id] {
				if len(blobs[id]) > remaining {
					file.BinaryKnown = false
					return fail(forge.DiffByteLimit, nil)
				}
				remaining -= len(blobs[id])
				seen[id] = true
			}
			file.Binary = file.Binary || strings.ContainsRune(blobs[id], '\x00')
		}
	}
	if len(result.Inventory.Reasons) == 0 {
		result.Inventory.Completeness = forge.DiffComplete
	}
	return result, nil
}

func diffBlobID(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))))
}
