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
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
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
	var permission interface{ HTTPStatusCode() int }
	var quota interface{ RetryAfter() time.Time }
	switch {
	case errors.Is(e.Cause, context.DeadlineExceeded):
		class = "timeout"
	case errors.As(e.Cause, &quota):
		class = "quota"
	case errors.As(e.Cause, &permission) && (permission.HTTPStatusCode() == 403 || permission.HTTPStatusCode() == 401):
		class = "permission denied"
	case errors.Is(e.Cause, context.Canceled):
		class = "canceled"
	}
	return "authority governing " + e.Boundary + ": " + class
}
func (e *GoverningReadError) Unwrap() error { return e.Cause }

// GoverningIdentityError is a definite block whose live identity/grant evidence
// can recover without a change to the governing tree.
type GoverningIdentityError struct{ Cause error }

func (e *GoverningIdentityError) Error() string { return e.Cause.Error() }
func (e *GoverningIdentityError) Unwrap() error { return e.Cause }

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
	if g.Coverage.Completeness != forge.DiffComplete || g.CommitSHA != commit {
		return &GoverningReadError{Boundary: "files", Cause: errors.New("incomplete governing provenance")}
	}
	validate := func(resolve classification.OwnerResolver) error {
		if c.Authority.Mode == authority.OpenMerge {
			return c.ValidateGoverning(branch, commit, g, resolve)
		}
		return classification.ValidateAuthorityOwners(commit, g, resolve)
	}
	failure := func(err error) error {
		return Errors{{NoLine: true, Path: "governing policy", Message: "authority governing validation failed: " + diagnostic.Sanitize(err.Error())}}
	}
	// Establish immutable coverage/schema independently of live eligibility. A
	// simultaneous revoked grant must not make invalid content appear mutable.
	if err := validate(func(classification.Owner) cp.IdentitySet {
		return cp.IdentitySet{Resolved: true, Fresh: true, IDs: []string{classification.GitHubUserID(1)}}
	}); err != nil {
		return failure(err)
	}
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
	err := validate(resolve)
	if readErr != nil {
		failure := &GoverningReadError{Boundary: "owners", Cause: readErr}
		// A definite missing/mismatched owner keeps the failure mutable even when
		// another owner has a transient read error; retry the whole acquisition.
		if errors.Is(readErr, forge.ErrIdentityUnavailable) || errors.Is(readErr, forge.ErrIdentityMismatch) {
			return &GoverningIdentityError{Cause: failure}
		}
		return failure
	}
	if err != nil {
		return &GoverningIdentityError{Cause: failure(err)}
	}

	return nil
}

// SanitizedAuthorityErrors preserves actionable locations without raw values or
// credentials crossing the authority diagnostic boundary.
func SanitizedAuthorityErrors(err error) error {
	var diagnostics Errors
	if errors.As(err, &diagnostics) {
		out := slices.Clone(diagnostics)
		for i := range out {
			out[i].NoLine = true
			out[i].Path = diagnostic.Sanitize(out[i].Path)
			out[i].Message = "invalid landed authority configuration: " + diagnostic.Sanitize(out[i].Message)
		}
		return out
	}
	return Errors{{NoLine: true, Path: "configuration", Message: "invalid landed authority configuration; repair the default-branch file"}}
}
