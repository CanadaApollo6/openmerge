// Package authority defines the transport-independent policy identities used
// by OpenMerge authority mode. Forge adapters resolve declared actors into the
// stable identifiers kept here; this package never assigns forge role IDs.
package authority

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Mode selects the policy authority for a repository. An omitted mode is
// GitHub for compatibility with existing configuration and digests.
type Mode string

// Supported authorities preserve the legacy GitHub default.
const (
	GitHub    Mode = "github"
	OpenMerge Mode = "openmerge"
)

const mandatoryPolicyOwners = "governing-codeowners-v1"

// Normalized returns GitHub for the omitted configuration spelling.
func (m Mode) Normalized() Mode {
	if m == "" {
		return GitHub
	}
	return m
}

// Validate rejects unrecognized policy authorities.
func (m Mode) Validate() error {
	switch m.Normalized() {
	case GitHub, OpenMerge:
		return nil
	default:
		return fmt.Errorf("unsupported policy authority %q", m)
	}
}

// Actor is the declared break-glass actor. Exactly one discriminator is set:
// a team name (org/team) or repository_admin: true. It deliberately has no
// user, bot, or membership representation.
type Actor struct {
	Team            string `json:"team,omitempty"`
	RepositoryAdmin bool   `json:"repository_admin,omitempty"`
}

// Validate checks the strict declared actor discriminant.
func (a Actor) Validate() error {
	if a.Team == "" && !a.RepositoryAdmin {
		return fmt.Errorf("break-glass actor requires exactly one of team or repository_admin: true")
	}
	if a.Team != "" && a.RepositoryAdmin {
		return fmt.Errorf("break-glass actor permits exactly one of team or repository_admin: true")
	}
	if a.Team != "" {
		if !validTeamName(a.Team) {
			return fmt.Errorf("break-glass team must be org/team")
		}
	}
	return nil
}

// UnmarshalJSON keeps the actor union strict even before its containing wire
// decoder validates required fields. In particular, a false role flag cannot
// be silently treated as omission alongside a team declaration.
func (a *Actor) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("break-glass actor must be an object")
	}
	var decoded Actor
	seen := map[string]bool{}
	teamDeclared := false
	repositoryAdminDeclared := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("break-glass actor key must be a string")
		}
		if seen[key] {
			return fmt.Errorf("duplicate break-glass actor key %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("break-glass actor %q must not be null", key)
		}
		switch key {
		case "team":
			teamDeclared = true
			if err := json.Unmarshal(raw, &decoded.Team); err != nil {
				return fmt.Errorf("break-glass actor team: %w", err)
			}
		case "repository_admin":
			repositoryAdminDeclared = true
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("break-glass actor repository_admin: %w", err)
			}
			if !value {
				return fmt.Errorf("repository_admin must be true when declared")
			}
			decoded.RepositoryAdmin = value
		default:
			return fmt.Errorf("unknown break-glass actor key %q", key)
		}
	}
	if token, err := decoder.Token(); err != nil {
		return err
	} else if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return fmt.Errorf("break-glass actor must be an object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("break-glass actor has trailing JSON data")
		}
		return err
	}
	if teamDeclared && repositoryAdminDeclared {
		return fmt.Errorf("break-glass actor permits exactly one of team or repository_admin: true")
	}
	if teamDeclared && decoded.Team == "" {
		return fmt.Errorf("break-glass team must not be empty")
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*a = decoded
	return nil
}

func validTeamName(name string) bool {
	organization, team, found := strings.Cut(name, "/")
	if !found || organization == "" || team == "" || strings.Contains(team, "/") {
		return false
	}
	return validTeamPart(organization, false) && validTeamPart(team, true)
}

func validTeamPart(part string, allowUnderscore bool) bool {
	if part == "" || !asciiAlphaNumeric(part[0]) || !asciiAlphaNumeric(part[len(part)-1]) {
		return false
	}
	for i := 1; i+1 < len(part); i++ {
		if asciiAlphaNumeric(part[i]) || part[i] == '-' || allowUnderscore && part[i] == '_' {
			continue
		}
		return false
	}
	return true
}

func asciiAlphaNumeric(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func opaqueID(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// ResolvedTeam is the stable team identity produced by the forge adapter. IDs
// are opaque values: this package does not assume numeric GitHub IDs.
type ResolvedTeam struct {
	OrganizationID string `json:"organization_id"`
	TeamID         string `json:"team_id"`
}

func (t ResolvedTeam) validate() error {
	if !opaqueID(t.OrganizationID) || !opaqueID(t.TeamID) {
		return fmt.Errorf("resolved team requires organization_id and team_id")
	}
	return nil
}

// ResolvedRepositoryAdmin is the documented repository-admin role identity
// resolved and validated by a forge adapter. RoleID remains opaque so callers
// cannot manufacture a guessed provider-specific numeric role.
type ResolvedRepositoryAdmin struct {
	RoleID string `json:"role_id"`
}

func (r ResolvedRepositoryAdmin) validate() error {
	if !opaqueID(r.RoleID) {
		return fmt.Errorf("resolved repository-admin actor requires role_id")
	}
	return nil
}

// ResolvedActor is the stable counterpart to Actor. Exactly one pointer is
// present, preserving the declared discriminated union through persistence and
// pure policy evaluation.
type ResolvedActor struct {
	Team            *ResolvedTeam            `json:"team,omitempty"`
	RepositoryAdmin *ResolvedRepositoryAdmin `json:"repository_admin,omitempty"`
}

// Validate checks the strict resolved actor discriminant.
func (a ResolvedActor) Validate() error {
	if a.Team == nil && a.RepositoryAdmin == nil {
		return fmt.Errorf("resolved break-glass actor requires exactly one of team or repository_admin")
	}
	if a.Team != nil && a.RepositoryAdmin != nil {
		return fmt.Errorf("resolved break-glass actor permits exactly one of team or repository_admin")
	}
	if a.Team != nil {
		return a.Team.validate()
	}
	return a.RepositoryAdmin.validate()
}

// ActorBinding binds the reviewed declaration to the forge-resolved stable
// identity. The adapter is responsible for resolving the declared team or
// repository-admin role; this validation ensures they cannot change kind.
type ActorBinding struct {
	Declared Actor         `json:"declared"`
	Resolved ResolvedActor `json:"resolved"`
}

// Validate checks both sides and their discriminant agreement.
func (b ActorBinding) Validate() error {
	if err := b.Declared.Validate(); err != nil {
		return err
	}
	if err := b.Resolved.Validate(); err != nil {
		return err
	}
	if (b.Declared.Team != "") != (b.Resolved.Team != nil) {
		return fmt.Errorf("declared and resolved break-glass actor kinds do not match")
	}
	return nil
}

// BypassIdentity maps the adapter-resolved break-glass actor to its canonical
// observation identity. Repository-admin roles use repository_role and the
// adapter's opaque RoleID verbatim; no provider numeric constant is assumed.
func (a ResolvedActor) BypassIdentity() (BypassActor, error) {
	if err := a.Validate(); err != nil {
		return BypassActor{}, err
	}
	if a.Team != nil {
		return BypassActor{Kind: "team", ID: a.Team.TeamID, Mode: "always"}, nil
	}
	return BypassActor{Kind: "repository_role", ID: a.RepositoryAdmin.RoleID, Mode: "always"}, nil
}
