package github

import (
	"context"
	"errors"

	gh "github.com/google/go-github/v93/github"

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

// ListBranches paginates through the shared conditional-GET transport, retaining
// per-page ETags and Link headers across 304 responses.
func (d *Driver) ListBranches(ctx context.Context, repo forge.RepoRef) ([]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	options := &gh.BranchListOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var branches []string
	for {
		page, response, err := d.client.Repositories.ListBranches(ctx, owner, name, options)
		if err != nil {
			return nil, safeError(err)
		}
		for _, branch := range page {
			branches = append(branches, branch.GetName())
		}
		if response.NextPage == 0 {
			return branches, nil
		}
		options.Page = response.NextPage
	}
}
