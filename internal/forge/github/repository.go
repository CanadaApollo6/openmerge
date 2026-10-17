package github

import (
	"context"
	"errors"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// DefaultBranch uses the installation's conditional, quota-aware metadata read.
func (d *Driver) DefaultBranch(ctx context.Context, repo forge.RepoRef) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	info, _, err := d.client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return "", safeError(err)
	}
	if info.GetDefaultBranch() == "" {
		return "", errors.New("repository default branch is missing")
	}
	return info.GetDefaultBranch(), nil
}
