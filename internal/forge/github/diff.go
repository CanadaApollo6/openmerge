package github

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
)

// RevisionDiffReader constructs a dormant read capability. cacheRoot is the
// server's --git-cache root, not a repository directory. The existing gitops
// constructor and locks select <root>/<installation>/<sha256(owner/repo)>.git,
// exactly as installationFactory does. Remote credentials stay in this driver.
// Construction installs no runtime consumer and makes no GitHub request.
func (d *Driver) RevisionDiffReader(ctx context.Context, cacheRoot string) (forge.RevisionDiffReader, error) {
	if cacheRoot == "" || d.installation <= 0 {
		return nil, errors.New("revision diff requires cache root and installation")
	}
	return gitops.New(ctx, gitops.Config{
		WorkDir: filepath.Join(cacheRoot, strconv.FormatInt(d.installation, 10)),
		Remote:  d.Remote, HeadRef: HeadRef,
	})
}

// GoverningFilesReader constructs the pinned, bounded provenance capability.
// It reuses the existing installation cache rather than the unbounded contents
// API, which does not expose modes or distinguish all tree entry kinds.
func (d *Driver) GoverningFilesReader(ctx context.Context, cacheRoot string) (forge.GoverningFilesReader, error) {
	reader, err := d.RevisionDiffReader(ctx, cacheRoot)
	if err != nil {
		return nil, err
	}
	return reader.(*gitops.Repo), nil
}
