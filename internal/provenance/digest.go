package provenance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// CanonicalDeclarations encodes declaration-digest v1 as compact Go JSON with
// fixed struct field order, HTML escaping, valid UTF-8, no whitespace or
// trailing newline. Sets are sorted and deduplicated; commits sort by object ID.
// Only complete original inventories are eligible. No checkpoint resolution,
// mutable review observation, policy digest or record generation is an input.
func CanonicalDeclarations(commits []Commit, complete bool, rules []Rule) ([]byte, error) {
	type digestCommit struct {
		ObjectID     string
		Class        Class
		Declarations []Declaration
	}
	type digestRule struct {
		When       string
		Conditions *string
		Approvals  *cp.Requirement
	}
	inventory := []digestCommit{}
	seen := map[string]bool{}
	if !complete {
		return nil, fmt.Errorf("incomplete declaration inventory")
	}
	for _, c := range commits {
		if !c.Complete || c.ObjectID == "" || !utf8.ValidString(c.ObjectID) || seen[c.ObjectID] {
			return nil, fmt.Errorf("incomplete, missing or duplicate original commit")
		}
		seen[c.ObjectID] = true
		for _, d := range c.Declarations {
			for _, s := range []string{d.Source, d.Version, d.Kind, d.Value, d.ValueDigest, d.RuleID} {
				if !utf8.ValidString(s) {
					return nil, fmt.Errorf("invalid UTF-8 declaration")
				}
			}
			if d.Source == "" || d.Version == "" || d.Kind == "" || d.Class != Agent && d.Class != Human && d.Class != Mixed || len(d.Value) > 4096 {
				return nil, fmt.Errorf("invalid declaration")
			}
			switch d.Outcome {
			case NotRequested, InvalidValue:
				if d.ValueDigest != "" {
					return nil, fmt.Errorf("unexpected value digest")
				}
			case ValueTooLong, InvalidEncoding:
				hash, err := hex.DecodeString(d.ValueDigest)
				if d.Value != "" || err != nil || len(hash) != 32 || strings.ToLower(d.ValueDigest) != d.ValueDigest {
					return nil, fmt.Errorf("invalid overlong value digest")
				}
			default:
				return nil, fmt.Errorf("resolution outcome cannot enter declaration digest")
			}
		}
		inventory = append(inventory, digestCommit{c.ObjectID, Classify(c.Declarations), canonicalDeclarations(c.Declarations)})
	}
	slices.SortFunc(inventory, func(a, b digestCommit) int { return strings.Compare(a.ObjectID, b.ObjectID) })
	definitions := []string{}
	attrs := DeclarationAttributes(commits, true)
	for _, r := range rules {
		if r.Conditions == nil && r.Approvals == nil {
			return nil, fmt.Errorf("empty requirement")
		}
		if r.Approvals != nil {
			if err := r.Approvals.Validate(); err != nil {
				return nil, err
			}
			if r.Approvals.Selector == cp.None {
				return nil, fmt.Errorf("none cannot be additive")
			}
		}
		if Truth(r.When, attrs) == cp.False {
			continue
		}
		entry := digestRule{When: r.When.String()}
		if r.Approvals != nil {
			a := *r.Approvals
			if a.Selector == cp.Human {
				a.Selector, a.Human = cp.AnyWriter, true
			}
			entry.Approvals = &a
		}
		if r.Conditions != nil {
			s := r.Conditions.String()
			entry.Conditions = &s
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, string(data))
	}
	slices.Sort(definitions)
	definitions = slices.Compact(definitions)
	return json.Marshal(struct {
		Version string
		Commits []digestCommit
		Rules   []string
	}{"openmerge-declarations-v1", inventory, definitions})
}

// DeclarationDigest is SHA-256 of CanonicalDeclarations, lowercase hexadecimal.
func DeclarationDigest(commits []Commit, complete bool, rules []Rule) (string, error) {
	data, err := CanonicalDeclarations(commits, complete, rules)
	if err != nil {
		return "", err
	}
	return valueDigest(string(data)), nil
}
