package changepolicy

import (
	"fmt"
	"strings"
)

// Selector names a pool; human is a filter, never a permission substitute.
type Selector string

// Selectors identify the supported approval pools.
const (
	None       Selector = "none"
	AnyWriter  Selector = "any_writer"
	Group      Selector = "group"
	Human      Selector = "human"
	CodeOwners Selector = "code_owners"
)

// Requirement has Count >= 1 except none and the rule-only CodeOwners selector.
// CodeOwners has no count until expanded into per-file obligations.
// All non-none selectors additionally require fresh identity-bound write access.
type Requirement struct {
	Selector       Selector
	Group          string
	Human, Receipt bool
	Count          int
}

// Validate checks the shared value contract independently of YAML parsing.
func (r Requirement) Validate() error {
	if r.Selector == None {
		if r.Group != "" || r.Human || r.Receipt || r.Count != 0 {
			return fmt.Errorf("none cannot have filters or a count")
		}
		return nil
	}
	if r.Selector == CodeOwners {
		if r.Group != "" || r.Human || r.Count != 0 {
			return fmt.Errorf("code_owners permits only receipt and no count")
		}
		return nil
	}
	if r.Count < 1 {
		return fmt.Errorf("count must be a positive integer")
	}
	switch r.Selector {
	case Group:
		if r.Group == "" || strings.TrimSpace(r.Group) != r.Group {
			return fmt.Errorf("group name must be nonempty and have no surrounding whitespace")
		}
	case AnyWriter, Human:
		if r.Group != "" {
			return fmt.Errorf("group and writer/human selectors are mutually exclusive")
		}
	default:
		return fmt.Errorf("invalid selector %q", r.Selector)
	}
	return nil
}

// Match ANDs facets. Lists mean all labels, every path, any touched path,
// and any author kind, respectively. Empty lists are rejected by the parser.
type Match struct {
	Labels, Paths, Touches []string
	Authors                []AccountKind
}

// Rule adds a requirement on a true OR unknown match.
type Rule struct {
	When    Match
	Require Requirement
}

// Approvals is nil for legacy queues. Default cannot be none or code_owners.
type Approvals struct {
	Default Requirement
	Rules   []Rule
}
