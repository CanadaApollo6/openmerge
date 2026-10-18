package fake

import (
	"context"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.GoverningFilesReader = (*Forge)(nil)

// GoverningFiles observes immutable regular-file snapshots, with no legacy state changes.
func (f *Forge) GoverningFiles(ctx context.Context, repo forge.RepoRef, req forge.GoverningFilesRequest) (forge.GoverningFiles, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	failed := func(reason forge.DiffFailureReason, err error) (forge.GoverningFiles, error) {
		return forge.GoverningFiles{CommitSHA: req.CommitSHA, Semantics: forge.DiffSemantics, Coverage: forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{reason}}}, err
	}
	if !forge.ValidDiffObjectID(req.CommitSHA) {
		return failed(forge.DiffInvalidObjectID, errors.New("governing read requires full commit object ID"))
	}
	if err := f.failure(ctx, "GoverningFiles"); err != nil {
		return failed(forge.DiffReadFailure, err)
	}
	r := f.repo(repo)
	if _, ok := r.commits[req.CommitSHA]; !ok {
		return failed(forge.DiffMissingObject, errors.New("missing governing commit"))
	}
	files := r.files(req.CommitSHA)
	return forge.CollectGoverningFiles(ctx, req, func(ctx context.Context, p string, budget int) (forge.GoverningFile, error) {
		file := forge.GoverningFile{Path: p, Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}}
		content, ok := files[p]
		if !ok {
			for name := range files {
				if strings.HasPrefix(name, p+"/") {
					file.Present, file.Mode = true, "040000"
					file.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{forge.DiffSpecialMode}}
					break
				}
			}
			return file, nil
		}
		file.Present, file.Mode, file.ObjectID = true, "100644", diffBlobID(content)
		if len(content) > budget {
			file.Coverage = forge.DiffCoverage{Completeness: forge.DiffIncomplete, Reasons: []forge.DiffFailureReason{forge.DiffByteLimit}}
		} else {
			file.Content = content
		}
		return file, ctx.Err()
	})
}
