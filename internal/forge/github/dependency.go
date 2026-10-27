package github

import (
	"context"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.DependencyReader = (*Driver)(nil)

// DependencySource is deliberately absent from production reconciliation. Like
// permission reads, it downgrades settlement contexts before using the shared
// installation transport. No cache-only lifecycle read is permitted.
func (d *Driver) DependencySource(ctx context.Context, repo forge.RepoRef, number int) (forge.DependencySource, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.DependencySource{}, err
	}
	if number <= 0 {
		return forge.DependencySource{}, forge.ErrIdentityUnavailable
	}
	pr, _, err := d.client.PullRequests.Get(identityContext(ctx), owner, name, number)
	if err != nil {
		return forge.DependencySource{}, safeError(err)
	}
	if pr == nil || pr.Number == nil || pr.GetNumber() != number || pr.State == nil || pr.GetState() != "open" && pr.GetState() != "closed" || pr.Merged == nil || pr.Body == nil || pr.GetBase().GetRepo().GetID() <= 0 {
		return forge.DependencySource{}, forge.ErrIdentityUnavailable
	}
	source := forge.DependencySource{
		RepositoryID: strconv.FormatInt(pr.GetBase().GetRepo().GetID(), 10),
		Number:       pr.GetNumber(), HeadSHA: pr.GetHead().GetSHA(), Base: pr.GetBase().GetRef(),
		BaseSHA: pr.GetBase().GetSHA(), Body: pr.GetBody(), Closed: pr.GetState() == "closed", Merged: pr.GetMerged(),
	}
	if !source.Valid() {
		return forge.DependencySource{}, forge.ErrIdentityUnavailable
	}
	return source, nil
}
