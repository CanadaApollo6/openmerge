package provenance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

// Policy is an immutable internal activation seam, not public configuration.
// Its digest must govern classification before any declaration-bound receipt is
// prepared. No constructor is called by the public config or runtime loaders.
type Policy struct {
	rules      []Rule
	classifier Classifier
	parser     ParserPolicy
	limits     InventoryLimits
	digest     string
}

// NewPolicy validates the no-network subset and binds every potential policy
// definition, including rules that are currently false. Membership is not a
// policy input. Ordinary expressions cannot contain declaration attributes.
func NewPolicy(baseline cp.Policy, rules []Rule, identities []IdentityRule, parser ParserPolicy, limits InventoryLimits, ordinary ...condition.Expression) (*Policy, error) {
	if baseline.Digest == "" || len(rules) == 0 || limits.Validate() != nil || parser != NewParserPolicy(parser.GitVersion) || parser.GitVersion == "" {
		return nil, fmt.Errorf("incomplete provenance policy")
	}
	if err := ValidateOrdinary(ordinary...); err != nil {
		return nil, err
	}
	classifier, err := NewClassifier(identities)
	if err != nil {
		return nil, err
	}
	p := &Policy{classifier: classifier, parser: parser, limits: limits}
	groups := map[string][]string{}
	for name := range baseline.Groups {
		groups[name] = nil
	}
	type definition struct {
		When, Conditions string
		Approvals        *cp.Requirement
	}
	definitions := []string{}
	for _, r := range rules {
		if err := r.Validate(baseline.Approvals != nil, groups); err != nil {
			return nil, err
		}
		d := definition{When: r.When.String()}
		expressions := []condition.Expression{r.When}
		if r.Conditions != nil {
			c := *r.Conditions
			r.Conditions = &c
			d.Conditions = c.String()
			expressions = append(expressions, c)
		}
		for _, e := range expressions {
			for _, a := range e.Attributes() {
				if a == "checkpoint-present" || a == "unresolved-checkpoints" {
					return nil, fmt.Errorf("%s requires the later checkpoint resolver slice", a)
				}
			}
		}
		if r.Approvals != nil {
			a := *r.Approvals
			if a.Selector == cp.Human {
				a.Selector, a.Human = cp.AnyWriter, true
			}
			r.Approvals, d.Approvals = &a, &a
		}
		p.rules = append(p.rules, r)
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, string(raw))
	}
	slices.Sort(definitions)
	identities = slices.Clone(classifier.rules)
	slices.SortFunc(identities, func(a, b IdentityRule) int { return strings.Compare(a.ID, b.ID) })
	ordinaryDefinitions := make([]string, len(ordinary))
	for i, e := range ordinary {
		ordinaryDefinitions[i] = e.String()
	}
	raw, err := json.Marshal(struct {
		Version, Baseline string
		Ordinary          []string
		Rules             []string
		Identities        []IdentityRule
		Parser            ParserPolicy
		Limits            InventoryLimits
	}{"openmerge/provenance-policy/v1", baseline.Digest, ordinaryDefinitions, slices.Compact(definitions), identities, parser, limits})
	if err != nil {
		return nil, err
	}
	p.digest = fmt.Sprintf("%x", sha256.Sum256(raw))
	return p, nil
}

func isProvenanceAttribute(a string) bool {
	return a == "provenance" || a == "agent-commits" || a == "unresolved-checkpoints" || a == "checkpoint-present"
}

// Digest includes the original policy identity and all declaration policy inputs.
func (p *Policy) Digest() string { return p.digest }

// ReaderInputs returns the fixed no-network reader policy.
func (p *Policy) ReaderInputs() (Classifier, ParserPolicy, InventoryLimits) {
	return p.classifier, p.parser, p.limits
}

// Rules returns detached definitions; condition.Expression itself is immutable.
func (p *Policy) Rules() []Rule {
	rules := slices.Clone(p.rules)
	for i, r := range rules {
		if r.Approvals != nil {
			a := *r.Approvals
			rules[i].Approvals = &a
		}
		if r.Conditions != nil {
			c := *r.Conditions
			rules[i].Conditions = &c
		}
	}
	return rules
}

// ValidateOrdinary rejects declarations even inside nested OR/NOT alternatives.
func ValidateOrdinary(ordinary ...condition.Expression) error {
	for _, e := range ordinary {
		for _, a := range e.Attributes() {
			if isProvenanceAttribute(a) {
				return fmt.Errorf("%s is only allowed in additive provenance requirements", a)
			}
		}
	}
	return nil
}
