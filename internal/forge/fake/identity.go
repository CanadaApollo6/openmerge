package fake

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var _ forge.IdentityReader = (*Forge)(nil)

type teamKey struct{ org, team int64 }

type identityState struct {
	users   map[string]forge.UserIdentity
	teams   map[string]forge.TeamIdentity
	members map[teamKey][]forge.UserIdentity
	grants  map[forge.RepoRef]map[int64]string
}

// SetIdentity models a rename or reassignment without changing legacy login
// permissions. New identity-bound permissions are configured by stable ID.
func (f *Forge) SetIdentity(user forge.UserIdentity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identities.users == nil {
		f.identities.users = map[string]forge.UserIdentity{}
	}
	for login, old := range f.identities.users {
		if old.ID == user.ID {
			delete(f.identities.users, login)
		}
	}
	f.identities.users[strings.ToLower(user.Login)] = user
}

// SetWriteGrant replaces the repository permission for one stable user ID.
func (f *Forge) SetWriteGrant(repo forge.RepoRef, userID int64, permission string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identities.grants == nil {
		f.identities.grants = map[forge.RepoRef]map[int64]string{}
	}
	if f.identities.grants[repo] == nil {
		f.identities.grants[repo] = map[int64]string{}
	}
	f.identities.grants[repo][userID] = permission
}

// SetTeam supplies the forge's effective membership, including inherited users.
func (f *Forge) SetTeam(team forge.TeamIdentity, members []forge.UserIdentity) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.identities.teams == nil {
		f.identities.teams = map[string]forge.TeamIdentity{}
		f.identities.members = map[teamKey][]forge.UserIdentity{}
	}
	key := teamKey{team.OrganizationID, team.ID}
	for name, old := range f.identities.teams {
		if (teamKey{old.OrganizationID, old.ID}) == key {
			delete(f.identities.teams, name)
		}
	}
	f.identities.teams[strings.ToLower(team.Organization+"/"+team.Slug)] = team
	f.identities.members[key] = slices.Clone(members)
}

// ResolveUser reads the current holder of a login.
func (f *Forge) ResolveUser(ctx context.Context, login string) (forge.UserIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls["ResolveUser"]++
	if err := f.failure(ctx, "ResolveUser"); err != nil {
		return forge.UserIdentity{}, err
	}
	user := f.identities.users[strings.ToLower(login)]
	if user.ID <= 0 || user.Login == "" {
		return forge.UserIdentity{}, forge.ErrIdentityUnavailable
	}
	return user, nil
}

// ResolveTeam reads the current team behind an org/team name.
func (f *Forge) ResolveTeam(ctx context.Context, name string) (forge.TeamIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls["ResolveTeam"]++
	if err := f.failure(ctx, "ResolveTeam"); err != nil {
		return forge.TeamIdentity{}, err
	}
	team := f.identities.teams[strings.ToLower(name)]
	if team.ID <= 0 || team.OrganizationID <= 0 {
		return forge.TeamIdentity{}, forge.ErrIdentityUnavailable
	}
	return team, nil
}

// TeamMembers returns independent, ID-deduplicated effective membership.
func (f *Forge) TeamMembers(ctx context.Context, team forge.TeamIdentity) ([]forge.UserIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls["TeamMembers"]++
	if err := f.failure(ctx, "TeamMembers"); err != nil {
		return nil, err
	}
	members, ok := f.identities.members[teamKey{team.OrganizationID, team.ID}]
	if !ok || team.ID <= 0 || team.OrganizationID <= 0 {
		return nil, forge.ErrIdentityUnavailable
	}
	byID := map[int64]forge.UserIdentity{}
	for _, user := range members {
		if user.ID <= 0 || user.Login == "" {
			return nil, forge.ErrIdentityUnavailable
		}
		byID[user.ID] = user
	}
	result := make([]forge.UserIdentity, 0, len(byID))
	for _, user := range byID {
		result = append(result, user)
	}
	slices.SortFunc(result, func(a, b forge.UserIdentity) int { return cmp.Compare(a.ID, b.ID) })
	return result, nil
}

// WriteGrant checks the login holder against the supplied stable author ID.
func (f *Forge) WriteGrant(ctx context.Context, repo forge.RepoRef, author forge.UserIdentity) (forge.WriteGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identityCalls["WriteGrant"]++
	if err := f.failure(ctx, "WriteGrant"); err != nil {
		return forge.WriteGrant{}, err
	}
	if author.ID <= 0 || author.Login == "" {
		return forge.WriteGrant{}, forge.ErrIdentityUnavailable
	}
	user := f.identities.users[strings.ToLower(author.Login)]
	permission, known := f.identities.grants[repo][user.ID]
	if !known {
		return forge.WriteGrant{}, forge.ErrIdentityUnavailable
	}
	grant := forge.WriteGrant{User: user, Permission: permission}
	if user.ID <= 0 || user.ID != author.ID {
		return grant, forge.ErrIdentityMismatch
	}
	return grant, nil
}
