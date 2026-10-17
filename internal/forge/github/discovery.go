package github

import (
	"context"
	"errors"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

// InstallationInfo is an App-authenticated access observation. Webhook payloads
// never create these observations; callers must finish the complete listing.
type InstallationInfo struct {
	ID        int64
	Suspended bool
}

// ListInstallations uses App authentication and the shared conditional transport.
// A failed page discards the entire result so absence cannot revoke access.
func (a *App) ListInstallations(ctx context.Context) ([]InstallationInfo, error) {
	ctx = forge.WithRequestClass(ctx, forge.DiscoveryRequest)
	opts := &gh.ListOptions{PerPage: 100}
	var result []InstallationInfo
	seen := make(map[int64]bool)
	for {
		page, res, err := a.client.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return nil, safeError(err)
		}
		for _, i := range page {
			if i.GetID() <= 0 {
				return nil, errors.New("github returned an invalid installation ID")
			}
			if seen[i.GetID()] {
				return nil, errors.New("github repeated an installation during pagination")
			}
			seen[i.GetID()] = true
			result = append(result, InstallationInfo{ID: i.GetID(), Suspended: !i.GetSuspendedAt().IsZero()})
		}
		if res.NextPage == 0 {
			return result, nil
		}
		if res.NextPage <= opts.Page {
			return nil, errors.New("github installation pagination did not advance")
		}
		opts.Page = res.NextPage
	}
}

// ListRepositories returns only complete installation-token listings. The
// transport retains ETags and pagination headers independently for every page.
func (d *Driver) ListRepositories(ctx context.Context) ([]string, error) {
	ctx = forge.WithRequestClass(ctx, forge.DiscoveryRequest)
	opts := &gh.ListOptions{PerPage: 100}
	var result []string
	expected := -1
	seen := make(map[string]bool)
	for {
		page, res, err := d.client.Apps.ListRepos(ctx, opts)
		if err != nil {
			return nil, safeError(err)
		}
		if page.TotalCount == nil || expected >= 0 && page.GetTotalCount() != expected {
			return nil, errors.New("github repository count changed during listing")
		}
		expected = page.GetTotalCount()
		for _, r := range page.Repositories {
			name := r.GetFullName()
			if _, _, err := splitRepo(forge.RepoRef{Path: name}); err != nil || strings.TrimSpace(name) != name {
				return nil, errors.New("github returned an invalid repository name")
			}
			folded := strings.ToLower(name)
			if seen[folded] {
				return nil, errors.New("github repeated a repository during pagination")
			}
			seen[folded] = true
			result = append(result, name)
		}
		if res.NextPage == 0 {
			if len(result) != expected {
				return nil, errors.New("github repository listing is incomplete")
			}
			return result, nil
		}
		if res.NextPage <= opts.Page {
			return nil, errors.New("github repository pagination did not advance")
		}
		opts.Page = res.NextPage
	}
}
