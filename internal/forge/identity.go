package forge

import (
	"context"
	"errors"
)

// AccountKind describes the forge account, independently of configured agent
// mappings. Unknown accounts must never be inferred to be human.
type AccountKind string

// Forge account kinds do not determine configured accountability or agent kind.
const (
	UnknownAccount AccountKind = "unknown"
	UserAccount    AccountKind = "user"
	BotAccount     AccountKind = "bot"
	AppAccount     AccountKind = "app"
)

// UserIdentity keeps a stable forge user ID and its observed display login.
// IDs are scoped to the forge instance; logins are never authorization keys.
type UserIdentity struct {
	ID    int64
	Login string
	Kind  AccountKind
}

// TeamIdentity pins both organization and team IDs so a renamed or reassigned
// slug cannot substitute another team's membership on a later read.
type TeamIdentity struct {
	ID, OrganizationID int64
	Organization, Slug string
}

// WriteGrant is a fresh permission observation and the identity returned in
// that same forge response. Permission alone is insufficient evidence.
type WriteGrant struct {
	User       UserIdentity
	Permission string
}

// CanWrite checks the grant against the review/comment author's stable ID.
// Missing IDs and unknown permission levels fail closed.
func (g WriteGrant) CanWrite(authorID int64) bool {
	if authorID <= 0 || g.User.ID != authorID {
		return false
	}
	switch g.Permission {
	case "admin", "maintain", "write":
		return true
	default:
		return false
	}
}

// Identity errors distinguish unavailable observations from mismatched grants
// and bounded, incomplete membership reads. None may authorize policy decisions.
var (
	ErrIdentityUnavailable  = errors.New("forge identity unavailable")
	ErrIdentityMismatch     = errors.New("forge grant identity does not match author")
	ErrMembershipIncomplete = errors.New("forge team membership incomplete")
)

// IdentityReader is optional and dormant until approval policy is activated.
// Errors mean unknown, never empty membership or a reusable previous grant.
// Every call revalidates with the forge; validated conditional reads are allowed.
// These reads must preserve settlement's reserved request capacity.
type IdentityReader interface {
	ResolveUser(context.Context, string) (UserIdentity, error)
	// ResolveTeam accepts org/team, without CODEOWNERS' leading @.
	ResolveTeam(context.Context, string) (TeamIdentity, error)
	// TeamMembers includes inherited members as exposed by the forge, deduped
	// by user ID. Success means the entire paginated observation was read.
	TeamMembers(context.Context, TeamIdentity) ([]UserIdentity, error)
	// WriteGrant authenticates author.ID against the permission response's ID.
	// On a mismatch it returns the observed identity with ErrIdentityMismatch;
	// callers must discard any grant on error. A missing author ID is invalid.
	// HTTP 404 (including an inaccessible repo or absent collaborator) is unknown,
	// returning ErrIdentityUnavailable, never a successful known-empty permission.
	// A successful non-writing permission must come from an identity-bound response.
	WriteGrant(context.Context, RepoRef, UserIdentity) (WriteGrant, error)
}
