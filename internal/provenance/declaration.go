// Package provenance defines ADR 0010's inert declaration contracts. All inputs
// are supplied by callers; this package performs no acquisition or runtime work.
package provenance

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// Class describes declarations, never authenticated authorship.
type Class string

// Declared classes are independent of submitting-account and approver kinds.
const (
	Agent             Class = "agent"
	Human             Class = "human"
	Mixed             Class = "mixed"
	Undeclared        Class = "undeclared"
	ParserVersion           = "git-trailers-v1"
	ClassifierVersion       = "mailbox-classes-v1"
)

// Outcome preserves invalid declarations and the no-network resolution state.
type Outcome string

// Declaration outcomes are extraction results, independent of resolution.
const (
	NotRequested    Outcome = "not_requested"
	InvalidValue    Outcome = "invalid_value"
	ValueTooLong    Outcome = "value_too_long"
	InvalidEncoding Outcome = "invalid_encoding"
)

// Declaration is normalized evidence from one original commit. Value is empty
// for overlong input; ValueDigest then identifies the entire unfolded value.
// Source and Version identify the extractor, not the neutral session's source.
type Declaration struct {
	Source      string
	Version     string
	Kind        string
	Value       string
	ValueDigest string
	Outcome     Outcome
	Class       Class
	RuleID      string
}

// Commit is a complete declaration observation of an original object. Complete
// includes every enabled extraction source, independently of resolution.
type Commit struct {
	ObjectID     string
	Complete     bool
	Declarations []Declaration
}

// Classify combines positive evidence. Malformed agent trailers remain agent
// evidence. Unknown identity strings do not implicitly declare human work.
func Classify(declarations []Declaration) Class {
	agent, human := false, false
	for _, d := range declarations {
		agent = agent || d.Class == Agent || d.Class == Mixed
		human = human || d.Class == Human || d.Class == Mixed
	}
	switch {
	case agent && human:
		return Mixed
	case agent:
		return Agent
	case human:
		return Human
	default:
		return Undeclared
	}
}

func canonicalDeclarations(in []Declaration) []Declaration {
	out := append([]Declaration{}, in...)
	slices.SortFunc(out, func(a, b Declaration) int {
		return strings.Compare(declarationKey(a), declarationKey(b))
	})
	return slices.Compact(out)
}

func declarationKey(d Declaration) string {
	// All fields are separated unambiguously even for malformed input.
	return fmt.Sprintf("%q", []string{d.Source, d.Version, d.Kind, d.Value, d.ValueDigest, string(d.Outcome), string(d.Class), d.RuleID})
}

func valueDigest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
