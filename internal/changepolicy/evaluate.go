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
	Approvals *Approvals
	Groups    map[string]IdentitySet
}

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

// RequirementResult explains one independent obligation. EligibleIDs includes
// native reviewers even for receipt-only requirements. Unresolved never means
// an empty pool; ApprovedIDs contains only usable evidence for this requirement.
// ReliedOn retains every counted stance (including sources and used receipts),
// ordered by user ID. BlockingStances retains current eligible requests for
// changes, even for receipt-only requirements. Receipt records are copied.
type RequirementResult struct {
	Origin                   string
	Requirement              Requirement
	EligibleIDs, ApprovedIDs []string
	Unresolved               bool
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
		if !validLabel(label) {
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

// Evaluate only consumes its arguments and never mutates them. Legacy queues
// return immediately without inspecting classification, grants or stances.
func Evaluate(policy Policy, in Input) Evaluation {
	e := Evaluation{Allowed: true}
	if policy.Approvals == nil {
		return e
	}
	e.OptedIn, e.ReviewsKnown = true, true
	addFailure := func(reason string) { e.Allowed = false; e.Reasons = append(e.Reasons, reason) }
	a := policy.Approvals
	if err := a.Default.Validate(); err != nil || a.Default.Selector == None || a.Default.Selector == CodeOwners {
		addFailure("invalid approvals.default")
		e.ReviewsKnown = false
		return e
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
	type obligation struct {
		origin      string
		requirement Requirement
		pool        *IdentitySet
	}
	var obligations []obligation
	codeEvidenceErr, policyEvidenceErr := codeOwnerEvidence(c), policyOwnerEvidence(c)
	if codeEvidenceErr != nil {
		pool := IdentitySet{Reason: "owner_evidence_incoherent: " + codeEvidenceErr.Error()}
		obligations = append(obligations, obligation{"code owners", Requirement{Selector: CodeOwners, Count: 1}, &pool})
	}
	definite := false
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
			if rule.Require.Selector != CodeOwners {
				obligations = append(obligations, obligation{origin: origin, requirement: rule.Require})
				continue
			}
			r := rule.Require
			r.Count = 1
			if codeEvidenceErr != nil {
				continue
			}
			if !c.InventoryComplete || !c.CodeOwnersKnown {
				pool := IdentitySet{Reason: "code owners or inventory unresolved; split incomplete changes"}
				obligations = append(obligations, obligation{origin + " code owners", r, &pool})
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
				obligations = append(obligations, obligation{origin + " code owner: " + owner.Path + " (" + string(owner.Side) + ")", r, &pool})
			}
		}
	}
	if !definite {
		obligations = append(obligations, obligation{origin: "default", requirement: a.Default})
	}
	if policyEvidenceErr != nil {
		pool := IdentitySet{Reason: "owner_evidence_incoherent: " + policyEvidenceErr.Error()}
		obligations = append(obligations, obligation{"policy owners", Requirement{Selector: AnyWriter, Count: 1}, &pool})
	} else if !c.InventoryComplete {
		pool := IdentitySet{Reason: "incomplete inventory: split the change to resolve policy owners"}
		obligations = append(obligations, obligation{"policy owners", Requirement{Selector: AnyWriter, Count: 1}, &pool})
	} else if c.Label("policy") != False || len(c.PolicyOwners) > 0 {
		if !c.PolicyOwnersKnown || len(c.PolicyOwners) == 0 {
			pool := IdentitySet{Reason: "policy paths or effective owners are unresolved"}
			obligations = append(obligations, obligation{"policy owners", Requirement{Selector: AnyWriter, Count: 1}, &pool})
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
		obligations = append(obligations, obligation{"owner: " + owner.Path, Requirement{Selector: AnyWriter, Count: 1}, &pool})
	}
	eligible := map[string]bool{}
	approved := map[string]bool{}
	for _, obligation := range obligations {
		r := obligation.requirement
		pool := obligation.pool
		if r.Selector == Group {
			value := policy.Groups[r.Group]
			pool = &value
		}
		result := RequirementResult{Origin: obligation.origin, Requirement: r, EligibleIDs: []string{}, ApprovedIDs: []string{}}
		if pool != nil && (!pool.Resolved || !pool.Fresh) {
			result.Unresolved = true
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

// Copy the receipt so persisted decision evidence does not alias caller memory.
func cloneStance(s Stance) Stance {
	if s.Receipt != nil {
		receipt := *s.Receipt
		s.Receipt = &receipt
	}
	return s
}
