package changepolicy

import (
	"fmt"
	"regexp"
	"strings"
)

// AccountKind is the effective kind after configured agent mappings.
type AccountKind string

// Effective account kinds distinguish humans from mapped or forge agents.
const (
	HumanAccount AccountKind = "human"
	AgentAccount AccountKind = "agent"
)

// Identity compares by forge ID; Login is display/condition evidence only.
type Identity struct {
	ID, Login string
	Kind      AccountKind
}

// WriteGrant binds the permission response ID to the requesting source author.
// A fresh response for a reassigned login cannot authorize the old ID.
type WriteGrant struct {
	SubjectID, ReturnedID string
	Fresh                 bool
	Write                 Truth
}

// Permits returns fresh write permission only for the same stable identity.
func (g WriteGrant) Permits(id string) Truth {
	if id == "" || !g.Fresh || g.SubjectID != id || g.ReturnedID != id || (g.Write != True && g.Write != False) {
		return Unknown
	}
	return g.Write
}

// Actor supplies identity and fresh permission evidence for a potential reviewer.
type Actor struct {
	Identity Identity
	Grant    WriteGrant
}

// Agent maps a configured account to its accountable human logins.
type Agent struct {
	Identity    string   `yaml:"identity"`
	Accountable []string `yaml:"accountable"`
}

// ResolvedIdentity is static policy identity resolution, not current membership.
// Names are bare logins or org/team; IDs include both users and teams.
type ResolvedIdentity struct {
	Name string      `json:"name"`
	ID   string      `json:"id"`
	Kind AccountKind `json:"kind"`
}

var loginPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\[bot\])?$`)
var teamPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?/[a-z0-9](?:[a-z0-9_-]*[a-z0-9])?$`)

func validLogin(s string) bool  { return loginPattern.MatchString(s) }
func validMember(s string) bool { return validLogin(s) || teamPattern.MatchString(s) }

// ValidateIdentities is called after resolution, before constructing a policy.
// Accountable humans need not have write permission. Mapped agents are agents
// even if their forge kind is human (including aliases sharing a stable ID).
func (r Repository) ValidateIdentities(resolved []ResolvedIdentity) error {
	byName := map[string]ResolvedIdentity{}
	for _, identity := range resolved {
		name := strings.ToLower(identity.Name)
		if identity.ID == "" {
			return fmt.Errorf("unresolved identity %q", name)
		}
		if _, exists := byName[name]; exists {
			return fmt.Errorf("duplicate resolved identity %q", name)
		}
		byName[name] = identity
	}
	agents := map[string]bool{}
	for _, agent := range r.Agents {
		identity, ok := byName[agent.Identity]
		if !ok {
			return fmt.Errorf("unresolved agent %q", agent.Identity)
		}
		agents[identity.ID] = true
	}
	for _, members := range r.Groups {
		for _, name := range members {
			if _, ok := byName[name]; !ok {
				return fmt.Errorf("unresolved group member %q", name)
			}
		}
	}
	for _, agent := range r.Agents {
		for _, name := range agent.Accountable {
			identity, ok := byName[name]
			if !ok || identity.Kind != HumanAccount || agents[identity.ID] {
				return fmt.Errorf("accountable identity %q must resolve to a human", name)
			}
		}
	}
	return nil
}

// EffectiveKind applies mappings by stable ID before the forge account kind.
func EffectiveKind(identity Identity, agentIDs []string) AccountKind {
	for _, id := range agentIDs {
		if id != "" && id == identity.ID {
			return AgentAccount
		}
	}
	return identity.Kind
}
