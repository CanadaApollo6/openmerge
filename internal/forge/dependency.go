package forge

import "context"

// CanRead evaluates the same fresh, identity-bound permission response used by
// CanWrite. App visibility and a permission without the declarer's ID grant
// nothing. Unknown/custom permission names fail closed.
func (g WriteGrant) CanRead(authorID int64) bool {
	if authorID <= 0 || g.User.ID != authorID {
		return false
	}
	switch g.Permission {
	case "read", "triage", "write", "maintain", "admin":
		return true
	default:
		return false
	}
}

// DependencySource is an all-or-error scoped lifecycle/body observation. It is
// separate from deployed PR/event payloads. Merged is never landing evidence.
type DependencySource struct {
	RepositoryID string
	Number       int
	HeadSHA      string
	Base         string
	BaseSHA      string
	Body         string
	Closed       bool
	Merged       bool
}

// Valid requires complete stable identity, head and base observations. Drivers
// must additionally reject missing wire fields, including false boolean values.
func (s DependencySource) Valid() bool {
	return s.RepositoryID != "" && s.Number > 0 && ValidDiffObjectID(s.HeadSHA) && s.Base != "" && ValidDiffObjectID(s.BaseSHA) && (!s.Merged || s.Closed)
}

// DependencyReader is an optional, unwired ADR 0011 capability. The caller must
// check the declarer's fresh CanRead grant BEFORE requesting source facts.
// Both methods use the installation budget and cannot spend settlement reserve.
type DependencyReader interface {
	WriteGrant(context.Context, RepoRef, UserIdentity) (WriteGrant, error)
	DependencySource(context.Context, RepoRef, int) (DependencySource, error)
}
