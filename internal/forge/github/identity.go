package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

var _ forge.IdentityReader = (*Driver)(nil)

// Identity reads never consume settlement's reservation, even when a landing
// preflight passes a settlement context. All use the installation transport.
func identityContext(ctx context.Context) context.Context {
	if forge.ClassOfRequest(ctx) == forge.SettlementRequest {
		return forge.WithRequestClass(ctx, forge.ActiveRequest)
	}
	return ctx
}

func validIdentityLogin(login string) bool {
	return simpleName(strings.TrimSuffix(login, "[bot]"))
}

func userIdentity(user *gh.User) forge.UserIdentity {
	kind := forge.UnknownAccount
	switch user.GetType() {
	case "User":
		kind = forge.UserAccount
	case "Bot":
		kind = forge.BotAccount
	case "App":
		kind = forge.AppAccount
	}
	return forge.UserIdentity{ID: user.GetID(), Login: user.GetLogin(), Kind: kind}
}

// ResolveUser freshly resolves a login without inferring human kind from it.
func (d *Driver) ResolveUser(ctx context.Context, login string) (forge.UserIdentity, error) {
	if !validIdentityLogin(login) {
		return forge.UserIdentity{}, forge.ErrIdentityUnavailable
	}
	user, _, err := d.client.Users.Get(identityContext(ctx), login)
	if err != nil {
		return forge.UserIdentity{}, identityError(err, false)
	}
	identity := userIdentity(user)
	if identity.ID <= 0 || !validIdentityLogin(identity.Login) {
		return forge.UserIdentity{}, forge.ErrIdentityUnavailable
	}
	return identity, nil
}

// ResolveTeam pins the organization and team IDs returned for an org/team name.
func (d *Driver) ResolveTeam(ctx context.Context, name string) (forge.TeamIdentity, error) {
	org, slug, ok := strings.Cut(name, "/")
	if !ok || !simpleName(org) || !simpleName(slug) {
		return forge.TeamIdentity{}, forge.ErrIdentityUnavailable
	}
	team, _, err := d.client.Teams.GetTeamBySlug(identityContext(ctx), org, slug)
	if err != nil {
		return forge.TeamIdentity{}, identityError(err, true)
	}
	identity := forge.TeamIdentity{ID: team.GetID(), OrganizationID: team.GetOrganization().GetID(), Organization: team.GetOrganization().GetLogin(), Slug: team.GetSlug()}
	if identity.ID <= 0 || identity.OrganizationID <= 0 || !ascii.EqualFold(identity.Organization, org) || !ascii.EqualFold(identity.Slug, slug) {
		return forge.TeamIdentity{}, forge.ErrIdentityUnavailable
	}
	return identity, nil
}

// TeamMembers includes child-team members exposed by GitHub. Read by immutable IDs,
// not the mutable org/team slug, and request all roles (including maintainers).
// Bound the observation at 10,000 members; overflow is unknown, never truncated.
func (d *Driver) TeamMembers(ctx context.Context, team forge.TeamIdentity) ([]forge.UserIdentity, error) {
	if team.ID <= 0 || team.OrganizationID <= 0 {
		return nil, forge.ErrIdentityUnavailable
	}
	ctx = identityContext(ctx)
	members := map[int64]forge.UserIdentity{}
	for page := 1; page != 0; {
		users, res, err := d.client.Teams.ListTeamMembersByID(ctx, team.OrganizationID, team.ID, &gh.TeamListTeamMembersOptions{Role: "all", ListOptions: gh.ListOptions{Page: page, PerPage: 100}})
		if err != nil {
			return nil, identityError(err, true)
		}
		for _, user := range users {
			identity := userIdentity(user)
			if identity.ID <= 0 || !validIdentityLogin(identity.Login) {
				return nil, forge.ErrIdentityUnavailable
			}
			members[identity.ID] = identity
		}
		if res.NextPage != 0 && (res.NextPage <= page || res.NextPage > 100) {
			return nil, forge.ErrMembershipIncomplete
		}
		page = res.NextPage
	}
	result := make([]forge.UserIdentity, 0, len(members))
	for _, member := range members {
		result = append(result, member)
	}
	slices.SortFunc(result, func(a, b forge.UserIdentity) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}

// WriteGrant binds one fresh permission response to the supplied author ID.
func (d *Driver) WriteGrant(ctx context.Context, repo forge.RepoRef, author forge.UserIdentity) (forge.WriteGrant, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.WriteGrant{}, err
	}
	if author.ID <= 0 || !validIdentityLogin(author.Login) {
		return forge.WriteGrant{}, forge.ErrIdentityUnavailable
	}
	level, _, err := d.client.Repositories.GetPermissionLevel(identityContext(ctx), owner, name, author.Login)
	if err != nil {
		normalized := safeError(err)
		var api *APIError
		if errors.As(normalized, &api) && api.StatusCode == 404 {
			return forge.WriteGrant{}, fmt.Errorf("%w: %w", forge.ErrIdentityUnavailable, normalized)
		}
		return forge.WriteGrant{}, normalized
	}
	grant := forge.WriteGrant{User: userIdentity(level.GetUser()), Permission: level.GetPermission()}
	if grant.User.ID <= 0 || grant.User.ID != author.ID {
		return grant, fmt.Errorf("%w: expected user %d, observed %d", forge.ErrIdentityMismatch, author.ID, grant.User.ID)
	}
	if !validIdentityLogin(grant.User.Login) {
		return grant, fmt.Errorf("%w: permission response has an invalid user login", forge.ErrIdentityMismatch)
	}
	return grant, nil
}

// Ordinary inaccessible policy identities block configuration until readable.
// Normalize rate limits first so permission-looking rate responses remain retryable.
func identityError(err error, team bool) error {
	normalized := safeError(err)
	var api *APIError
	if errors.As(normalized, &api) && (api.StatusCode == 404 || team && api.StatusCode == 403) {
		return fmt.Errorf("%w: %w", forge.ErrIdentityUnavailable, normalized)
	}
	return normalized
}
