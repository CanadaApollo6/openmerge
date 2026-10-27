package changepolicy

import (
	"fmt"
	"maps"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

// Policy contains one queue's policy and fresh membership observations.
// Static resolved names/IDs belong in the digest; memberships and grants do not.
type Policy struct {
	// RequirePolicyOwners enables authority decisions without an approvals floor.
	RequirePolicyOwners bool `json:",omitempty"`
	// Digest is the semantic PolicyDigest computed by the configuration boundary.
	Digest    string
	Approvals *Approvals
	Groups    map[string]IdentitySet
}

// Enabled reports whether immutable classification and decisions are required.
func (p Policy) Enabled() bool { return p.Approvals != nil || p.RequirePolicyOwners }

// Input contains already folded current stances, indexed by stable user ID.
// ExcludedIDs includes the author, submitter and every accountable human.
// PR 6 owns ordering/attribution; PR 7a supplies the current validated binding.
type Input struct {
	Classification Classification
	AuthorKind     AccountKind
	Current        ReceiptIdentity
	ExcludedIDs    []string
	Actors         map[string]Actor
	Stances        map[string]Stance
}

// ObligationCause records why an independent requirement exists. Consumers use
// it and RuleIndex rather than parsing the human-readable Origin.
type ObligationCause string

// Typed origins of independently expanded requirements.
const (
	CauseDefault             ObligationCause = "default"
	CauseRule                ObligationCause = "rule"
	CauseRuleCodeOwners      ObligationCause = "rule_code_owners"
	CauseCodeOwnerEvidence   ObligationCause = "code_owner_evidence"
	CausePolicyOwnerEvidence ObligationCause = "policy_owner_evidence"
	CausePolicyOwner         ObligationCause = "policy_owner"
)

// UnresolvedCause classifies a non-final requirement result without relying on
// Origin prose. ExcludedOwner is known failure rather than uncertainty.
type UnresolvedCause string

// Stable reasons a requirement has not been met.
const (
	UnresolvedInventory   UnresolvedCause = "unknown_inventory"
	UnresolvedOwners      UnresolvedCause = "unknown_owners"
	UnresolvedEligibility UnresolvedCause = "unknown_eligibility"
	ExcludedOwner         UnresolvedCause = "excluded_owner"
)

// RequirementResult explains one independent obligation. EligibleIDs includes
// native reviewers even for receipt-only requirements. Unresolved never means
// an empty pool; ApprovedIDs contains only usable evidence for this requirement.
// ReliedOn retains every counted stance (including sources and used receipts),
// ordered by user ID. BlockingStances retains current eligible requests for
// changes, even for receipt-only requirements. Receipt records are copied.
type RequirementResult struct {
	Origin                   string
	Cause                    ObligationCause
	RuleIndex                *int
	Path                     string
	Side                     OwnerSide
	PoolIDs                  []string
	Requirement              Requirement
	EligibleIDs, ApprovedIDs []string
	Unresolved               bool
	UnresolvedCause          UnresolvedCause
	InventoryAffected        bool
	Reason                   string
	Met                      bool
	ReliedOn                 []Stance
	BlockingStances          []Stance
}

// Evaluation carries approval evidence for the gate and condition snapshot.
// No opted-in requirement means a known empty review set, not an implicit pool.
type Evaluation struct {
	OptedIn, Allowed bool
	Requirements     []RequirementResult
	BlockingIDs      []string
	ReviewLogins     []string
	ReviewsKnown     bool
	Reasons          []string
}

// ReviewAttribute preserves the condition grammar, including nested negation.
// The zero Value is unknown; Strings() is a known empty list.
func (e Evaluation) ReviewAttribute() condition.Value {
	if !e.OptedIn || !e.ReviewsKnown {
		return condition.Value{}
	}
	return condition.Strings(e.ReviewLogins...)
}

// MatchRule ANDs facets with Kleene truth values. A false facet rules out a
// rule even if another facet is unknown. Whole-path globs are shared with
// conditions; CODEOWNERS patterns are deliberately not evaluated here.
func MatchRule(m Match, c Classification, author AccountKind) (Truth, error) {
	result := True
	if len(m.Labels)+len(m.Paths)+len(m.Touches)+len(m.Authors) == 0 {
		return Unknown, fmt.Errorf("empty match")
	}
	for _, label := range m.Labels {
		if !ValidClassificationLabel(label) {
			return Unknown, fmt.Errorf("invalid classification label %q", label)
		}
		result = and(result, c.Label(label))
	}
	for _, facet := range []struct {
		globs []string
		every bool
	}{{m.Paths, true}, {m.Touches, false}} {
		if len(facet.globs) == 0 {
			continue
		}
		patterns := make([]func(string) bool, 0, len(facet.globs))
		for _, glob := range facet.globs {
			if glob == "" {
				return Unknown, fmt.Errorf("empty glob")
			}
			pattern, err := condition.CompileGlob(glob)
			if err != nil {
				return Unknown, err
			}
			patterns = append(patterns, pattern.MatchString)
		}
		value := False
		if facet.every {
			value = True
		}
		for _, path := range c.Paths {
			hit := slices.ContainsFunc(patterns, func(matches func(string) bool) bool { return matches(path) })
			if facet.every && !hit {
				value = False
			}
			if !facet.every && hit {
				value = True
			}
		}
		if facet.every && len(c.Paths) == 0 {
			value = Unknown
		}
		if !c.InventoryComplete || !c.PathsSafe {
			if facet.every || value != True {
				value = Unknown
			}
		}
		result = and(result, value)
	}
	if len(m.Authors) > 0 {
		value := False
		for _, kind := range m.Authors {
			if kind != HumanAccount && kind != AgentAccount {
				return Unknown, fmt.Errorf("invalid author kind %q", kind)
			}
		}
		if author != HumanAccount && author != AgentAccount {
			value = Unknown
		} else if slices.Contains(m.Authors, author) {
			value = True
		}
		result = and(result, value)
	}
	return result, nil
}

// inventoryAffected reports whether an otherwise possible rule exists only
// because an incomplete inventory prevented a definite result.
func inventoryAffected(m Match, c Classification, author AccountKind, match Truth) bool {
	if c.InventoryComplete || match != Unknown {
		return false
	}
	complete := c
	complete.InventoryComplete = true
	withoutInventoryGap, err := MatchRule(m, complete, author)
	return err == nil && withoutInventoryGap != Unknown
}

type obligation struct {
	origin            string
	cause             ObligationCause
	ruleIndex         *int
	requirement       Requirement
	pool              *IdentitySet
	path              string
	side              OwnerSide
	inventoryAffected bool
}

// Evaluate only consumes its arguments and never mutates them. Legacy queues
// return immediately without inspecting classification, grants or stances.
func Evaluate(policy Policy, in Input) Evaluation {
	e := Evaluation{Allowed: true}
	if !policy.Enabled() {
		return e
	}
	e.OptedIn, e.ReviewsKnown = true, true
	addFailure := func(reason string) { e.Allowed = false; e.Reasons = append(e.Reasons, reason) }
	a := policy.Approvals
	if a != nil {
		if err := a.Default.Validate(); err != nil || a.Default.Selector == None || a.Default.Selector == CodeOwners {
			addFailure("invalid approvals.default")
			e.ReviewsKnown = false
			return e
		}
	}
	c := in.Classification
	if !c.Inputs.Recorded() || c.Inputs.ChangeID != in.Current.ChangeID ||
		c.Inputs.Revision != in.Current.Revision || c.Inputs.Base != in.Current.Base ||
		c.Inputs.Head != in.Current.Head || c.Inputs.MergeBase != in.Current.MergeBase ||
		c.Inputs.PolicyDigest != in.Current.PolicyDigest || c.Inputs.Generation != in.Current.Generation {
		addFailure("classification basis is unresolved or stale")
		e.ReviewsKnown = false
		return e
	}
	var obligations []obligation
	codeEvidenceErr, policyEvidenceErr := codeOwnerEvidence(c), policyOwnerEvidence(c)
	if codeEvidenceErr != nil {
		pool := IdentitySet{Reason: "owner_evidence_incoherent: " + codeEvidenceErr.Error()}
		obligations = append(obligations, obligation{origin: "code owners", cause: CauseCodeOwnerEvidence, requirement: Requirement{Selector: CodeOwners, Count: 1}, pool: &pool})
	}
	definite := false
	if a != nil {
		for i, rule := range a.Rules {
			if err := rule.Require.Validate(); err != nil {
				addFailure("invalid rule requirement")
				e.ReviewsKnown = false
				continue
			}
			match, err := MatchRule(rule.When, in.Classification, in.AuthorKind)
			if err != nil {
				addFailure("invalid rule match: " + err.Error())
				e.ReviewsKnown = false
				continue
			}
			if match == True {
				definite = true
			}
			if match != False && rule.Require.Selector != None {
				origin := fmt.Sprintf("rules[%d]", i)
				ruleIndex := i
				if rule.Require.Selector != CodeOwners {
					obligations = append(obligations, obligation{origin: origin, cause: CauseRule, ruleIndex: &ruleIndex, requirement: rule.Require, inventoryAffected: inventoryAffected(rule.When, c, in.AuthorKind, match)})
					continue
				}
				if codeEvidenceErr == nil {
					obligations = append(obligations, codeOwnerObligations(rule.Require, c, origin, &ruleIndex)...)
				}
			}
		}
		if !definite {
			obligations = append(obligations, obligation{origin: "default", cause: CauseDefault, requirement: a.Default})
		}
	}
	if policyEvidenceErr != nil {
		pool := IdentitySet{Reason: "owner_evidence_incoherent: " + policyEvidenceErr.Error()}
		obligations = append(obligations, obligation{origin: "policy owners", cause: CausePolicyOwnerEvidence, requirement: Requirement{Selector: AnyWriter, Count: 1}, pool: &pool})
	} else if !c.InventoryComplete {
		pool := IdentitySet{Reason: "incomplete inventory: split the change to resolve policy owners"}
		obligations = append(obligations, obligation{origin: "policy owners", cause: CausePolicyOwnerEvidence, requirement: Requirement{Selector: AnyWriter, Count: 1}, pool: &pool, inventoryAffected: true})
	} else if c.Label("policy") != False || len(c.PolicyOwners) > 0 {
		if !c.PolicyOwnersKnown || len(c.PolicyOwners) == 0 {
			pool := IdentitySet{Reason: "policy paths or effective owners are unresolved"}
			obligations = append(obligations, obligation{origin: "policy owners", cause: CausePolicyOwnerEvidence, requirement: Requirement{Selector: AnyWriter, Count: 1}, pool: &pool})
		}
	}
	for _, owner := range c.PolicyOwners {
		if policyEvidenceErr != nil {
			break
		}
		pool := owner.Eligible
		if owner.Path == "" || len(pool.IDs) == 0 {
			pool.Resolved = false
			if pool.Reason == "" {
				pool.Reason = "policy path has no resolvable owners"
			}
		}
		obligations = append(obligations, obligation{origin: "owner: " + owner.Path, cause: CausePolicyOwner, requirement: Requirement{Selector: AnyWriter, Count: 1}, pool: &pool, path: owner.Path})
	}
	return evaluateObligations(e, obligations, policy.Groups, in)
}

func evaluateObligations(e Evaluation, obligations []obligation, groups map[string]IdentitySet, in Input) Evaluation {
	addFailure := func(reason string) { e.Allowed = false; e.Reasons = append(e.Reasons, reason) }
	eligible := map[string]bool{}
	approved := map[string]bool{}
	for _, obligation := range obligations {
		r := obligation.requirement
		pool := obligation.pool
		if r.Selector == Group {
			value := groups[r.Group]
			pool = &value
		}
		result := RequirementResult{Path: obligation.path, Side: obligation.side, Origin: obligation.origin, Cause: obligation.cause, RuleIndex: obligation.ruleIndex, Requirement: r, InventoryAffected: obligation.inventoryAffected, EligibleIDs: []string{}, ApprovedIDs: []string{}}
		setUnresolvedCause := func(cause UnresolvedCause) {
			if result.UnresolvedCause == "" {
				result.UnresolvedCause = cause
			}
		}
		if pool != nil {
			result.PoolIDs = slices.Clone(pool.IDs)
			slices.Sort(result.PoolIDs)
		}
		if pool != nil && (!pool.Resolved || !pool.Fresh) {
			result.Unresolved = true
			switch {
			case obligation.inventoryAffected:
				setUnresolvedCause(UnresolvedInventory)
			case !pool.Resolved && ownerRelated(obligation.cause):
				setUnresolvedCause(UnresolvedOwners)
			default:
				setUnresolvedCause(UnresolvedEligibility)
			}
			result.Reason = pool.Reason
			if result.Reason == "" {
				result.Reason = "membership unresolved or not freshly revalidated"
			}
		}
		// Only current stance holders and members of this obligation's pool
		// can affect it. A cache entry alone is not a potential approval.
		ids := map[string]bool{}
		for id, stance := range in.Stances {
			if stance.Revision == in.Current.Revision && stance.Head == in.Current.Head &&
				(stance.Kind == Approve || stance.Kind == RequestChanges) {
				ids[id] = true
			}
		}
		if pool != nil {
			for _, id := range pool.IDs {
				if _, observed := in.Actors[id]; observed {
					ids[id] = true
				}
			}
		}
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			actor := in.Actors[id]
			v := eligibility(id, actor, r, pool, in.ExcludedIDs)
			if v == Unknown {
				result.Unresolved = true
				setUnresolvedCause(UnresolvedEligibility)
			}
			if v != True {
				continue
			}
			eligible[id] = true
			result.EligibleIDs = append(result.EligibleIDs, id)
			stance, exists := in.Stances[id]
			if !exists || stance.UserID != id || stance.Revision != in.Current.Revision || stance.Head != in.Current.Head {
				continue
			}
			if stance.Kind == RequestChanges {
				e.BlockingIDs = append(e.BlockingIDs, id)
				result.BlockingStances = append(result.BlockingStances, cloneStance(stance))
			}
			if stance.Kind != Approve {
				continue
			}
			if stance.Source.ID == "" ||
				(stance.Source.Channel != NativeReview && stance.Source.Channel != CommentCommand) ||
				(stance.Source.Channel == CommentCommand && stance.Receipt == nil) ||
				(stance.Source.Channel == NativeReview && stance.Receipt != nil) {
				result.Unresolved = true
				setUnresolvedCause(UnresolvedEligibility)
				continue
			}
			validReceipt := stance.Receipt != nil && in.Current.Recorded() && *stance.Receipt == in.Current
			// An invalid latest receipt never falls back to an older native review.
			if stance.Receipt != nil && !validReceipt {
				continue
			}
			approved[id] = true
			if !r.Receipt || validReceipt {
				result.ApprovedIDs = append(result.ApprovedIDs, id)
				result.ReliedOn = append(result.ReliedOn, cloneStance(stance))
			}
		}
		slices.Sort(result.EligibleIDs)
		slices.Sort(result.ApprovedIDs)
		result.Met = !result.Unresolved && len(result.ApprovedIDs) >= r.Count
		if !result.Met && !result.Unresolved {
			switch {
			case obligation.inventoryAffected:
				result.UnresolvedCause = UnresolvedInventory
			case result.Path != "" && len(result.EligibleIDs) == 0 && len(result.PoolIDs) > 0 && allExcluded(result.PoolIDs, in.ExcludedIDs):
				result.UnresolvedCause = ExcludedOwner
			}
		}
		if !result.Met {
			if result.Reason == "" {
				if result.Unresolved {
					result.Reason = "reviewer eligibility unresolved"
				} else {
					result.Reason = fmt.Sprintf("waiting for %d eligible approvals; have %d", r.Count, len(result.ApprovedIDs))
				}
			}
			addFailure(result.Origin + ": " + result.Reason)
		}
		if result.Unresolved {
			e.ReviewsKnown = false
		}
		e.Requirements = append(e.Requirements, result)
	}
	for id := range approved {
		if eligible[id] {
			e.ReviewLogins = append(e.ReviewLogins, in.Actors[id].Identity.Login)
		}
	}
	e.ReviewLogins = sortedSet(e.ReviewLogins)
	e.BlockingIDs = sortedSet(e.BlockingIDs)
	if len(e.BlockingIDs) > 0 {
		addFailure("eligible reviewer requests changes")
	}
	return e
}

func eligibility(id string, actor Actor, r Requirement, pool *IdentitySet, excluded []string) Truth {
	if slices.Contains(excluded, id) {
		return False
	}
	if pool != nil && pool.Resolved && pool.Fresh && !slices.Contains(pool.IDs, id) {
		return False
	}
	if actor.Grant.Permits(id) == False {
		return False
	}
	if id == "" || actor.Identity.ID != id || actor.Identity.Login == "" {
		return Unknown
	}
	if r.Human || r.Selector == Human {
		if actor.Identity.Kind == AgentAccount {
			return False
		}
		if actor.Identity.Kind != HumanAccount {
			return Unknown
		}
	}
	if pool != nil && (!pool.Resolved || !pool.Fresh) {
		return Unknown
	}
	return actor.Grant.Permits(id)
}

func ownerRelated(cause ObligationCause) bool {
	switch cause {
	case CauseRuleCodeOwners, CauseCodeOwnerEvidence, CausePolicyOwnerEvidence, CausePolicyOwner:
		return true
	default:
		return false
	}
}

func allExcluded(ids, excluded []string) bool {
	return len(ids) > 0 && !slices.ContainsFunc(ids, func(id string) bool { return !slices.Contains(excluded, id) })
}

// Copy the receipt so persisted decision evidence does not alias caller memory.
func cloneStance(s Stance) Stance {
	if s.Receipt != nil {
		receipt := *s.Receipt
		s.Receipt = &receipt
	}
	return s
}

// EvaluateRequirement evaluates just one additive approval obligation. It uses
// the ordinary exclusions, receipts, fresh grants and vetoes, but imports no
// default, exemption or policy-owner pool from another layer.
func EvaluateRequirement(r Requirement, groups map[string]IdentitySet, in Input) Evaluation {
	e := Evaluation{OptedIn: true, Allowed: true, ReviewsKnown: true}
	if err := r.Validate(); err != nil || r.Selector == None || !classificationMatches(in) {
		return Evaluation{OptedIn: true, Reasons: []string{"additive approval inputs unresolved or invalid"}}
	}
	obligations := []obligation{{origin: "additional", cause: CauseRule, requirement: r}}
	if r.Selector == CodeOwners {
		if err := codeOwnerEvidence(in.Classification); err != nil {
			pool := IdentitySet{Reason: "owner_evidence_incoherent: " + err.Error()}
			obligations = []obligation{{origin: "code owners", cause: CauseCodeOwnerEvidence, requirement: r, pool: &pool}}
		} else {
			obligations = codeOwnerObligations(r, in.Classification, "additional", nil)
		}
	}
	return evaluateObligations(e, obligations, groups, in)
}

func classificationMatches(in Input) bool {
	c := in.Classification.Inputs
	return c.Recorded() && c.ChangeID == in.Current.ChangeID && c.Revision == in.Current.Revision &&
		c.Base == in.Current.Base && c.Head == in.Current.Head && c.MergeBase == in.Current.MergeBase &&
		c.PolicyDigest == in.Current.PolicyDigest && c.Generation == in.Current.Generation
}

func codeOwnerObligations(requirement Requirement, c Classification, origin string, ruleIndex *int) []obligation {
	var obligations []obligation
	r := requirement
	r.Count = 1
	if !c.InventoryComplete || !c.CodeOwnersKnown {
		pool := IdentitySet{Reason: "code owners or inventory unresolved; split incomplete changes"}
		obligations = append(obligations, obligation{origin: origin + " code owners", cause: CauseRuleCodeOwners, ruleIndex: ruleIndex, requirement: r, pool: &pool, inventoryAffected: !c.InventoryComplete})
	}
	for _, owner := range c.CodeOwners {
		if owner.Status == OwnersUncovered || owner.Status == OwnersOwnerless {
			continue
		}
		pool := owner.Eligible
		if owner.Status != OwnersResolved || owner.Path == "" ||
			(owner.Side != OldSide && owner.Side != NewSide) ||
			owner.LastMatchingEntry == "" || len(pool.IDs) == 0 {
			pool.Resolved = false
			if pool.Reason == "" {
				pool.Reason = "code owner entry unresolved"
			}
		}
		obligations = append(obligations, obligation{origin: origin + " code owner: " + owner.Path + " (" + string(owner.Side) + ")", cause: CauseRuleCodeOwners, ruleIndex: ruleIndex, requirement: r, pool: &pool, path: owner.Path, side: owner.Side})
	}
	return obligations
}
