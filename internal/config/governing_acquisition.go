package config

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// GoverningReadError retains retry classification without exposing transport text.
// No caller-provided identity set can satisfy acquisition.
type GoverningReadError struct {
	Boundary string
	Cause    error
}

func (e *GoverningReadError) Error() string {
	class := "unavailable"
	var quota interface{ RetryAfter() time.Time }
	switch {
	case errors.Is(e.Cause, context.DeadlineExceeded):
		class = "timeout"
	case errors.As(e.Cause, &quota):
		class = "quota"
	case errors.Is(e.Cause, context.Canceled):
		class = "canceled"
	}
	return "authority governing " + e.Boundary + ": " + class
}
func (e *GoverningReadError) Unwrap() error { return e.Cause }

// AcquireGoverning validates immutable landed content and freshly resolved,
// write-eligible owners. The server selects branch/commit; proposed files and
// draft verification are never substitutes for this read.
func (c AuthorityCandidate) AcquireGoverning(ctx context.Context, repo forge.RepoRef, branch, commit string, files forge.GoverningFilesReader, ids forge.IdentityReader) error {
	if files == nil || ids == nil {
		return errors.New("authority governing readers unavailable")
	}
	g, err := files.GoverningFiles(ctx, repo, forge.GoverningFilesRequest{CommitSHA: commit, PathsComplete: true, AllAttributes: true})
	if err != nil {
		return &GoverningReadError{Boundary: "files", Cause: err}
	}
	return c.ValidateGoverningIdentities(ctx, repo, branch, commit, g, ids)
}

// ValidateGoverningIdentities also serves bounded tree-backed operator readers.
func (c AuthorityCandidate) ValidateGoverningIdentities(ctx context.Context, repo forge.RepoRef, branch, commit string, g forge.GoverningFiles, ids forge.IdentityReader) error {
	var readErr error
	resolve := func(o classification.Owner) cp.IdentitySet {
		unknown := cp.IdentitySet{Reason: "governing owner unresolved"}
		if ids == nil {
			return unknown
		}
		var users []forge.UserIdentity
		switch o.Kind {
		case "team":
			org, slug, ok := strings.Cut(o.Name, "/")
			if !ok {
				return unknown
			}
			team, err := ids.ResolveTeam(ctx, o.Name)
			if err != nil {
				readErr = errors.Join(readErr, err)
				return unknown
			}
			if team.ID <= 0 || team.OrganizationID <= 0 || !strings.EqualFold(team.Organization, org) || !strings.EqualFold(team.Slug, slug) {
				return unknown
			}
			users, err = ids.TeamMembers(ctx, team)
			if err != nil {
				readErr = errors.Join(readErr, err)
				return unknown
			}
		case "user":
			user, err := ids.ResolveUser(ctx, o.Name)
			if err != nil {
				readErr = errors.Join(readErr, err)
				return unknown
			}
			if !strings.EqualFold(user.Login, o.Name) {
				return unknown
			}
			users = []forge.UserIdentity{user}
		default:
			return unknown
		}
		pool := cp.IdentitySet{Resolved: true, Fresh: true, IDs: []string{}}
		for _, user := range users {
			if user.ID <= 0 || user.Kind != forge.UserAccount {
				return unknown
			}
			grant, err := ids.WriteGrant(ctx, repo, user)
			if err != nil {
				readErr = errors.Join(readErr, err)
				return unknown
			}
			if grant.User.ID != user.ID {
				return unknown
			}
			if grant.CanWrite(user.ID) {
				pool.IDs = append(pool.IDs, classification.GitHubUserID(user.ID))
			}
		}
		slices.Sort(pool.IDs)
		pool.IDs = slices.Compact(pool.IDs)
		return pool
	}
	var err error
	if c.Authority.Mode == authority.OpenMerge {
		err = c.ValidateGoverning(branch, commit, g, resolve)
	} else {
		err = classification.ValidateAuthorityOwners(commit, g, resolve)
	}
	if readErr != nil {
		return &GoverningReadError{Boundary: "owners", Cause: readErr}
	}
	if err != nil {
		return errors.New("authority governing validation failed: require complete landed config, exactly one default queue, supported selectors and eligible CODEOWNERS coverage for all policy paths")
	}
	return nil
}
