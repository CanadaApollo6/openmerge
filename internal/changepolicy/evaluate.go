package changepolicy

import (
	"fmt"
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
type RequirementResult struct {
	Origin                   string
	Requirement              Requirement
	EligibleIDs, ApprovedIDs []string
	Unresolved               bool
	Reason                   string
	Met                      bool
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
		result = and(result, c.label(label))
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
	if err := a.Default.Validate(); err != nil || a.Default.Selector == None {
		addFailure("invalid approvals.default")
		e.ReviewsKnown = false
		return e
	}
	if in.Current.Head == "" || in.Current.Revision == 0 {
		addFailure("current revision and head are unresolved")
	}
	type obligation struct {
		origin      string
		requirement Requirement
		pool        *IdentitySet
	}
	var obligations []obligation
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
			obligations = append(obligations, obligation{origin: fmt.Sprintf("rules[%d]", i), requirement: rule.Require})
		}
	}
	if !definite {
		obligations = append(obligations, obligation{origin: "default", requirement: a.Default})
	}
	c := in.Classification
	if c.Inputs.Head != in.Current.Head || c.Inputs.PolicyDigest != in.Current.PolicyDigest ||
		c.Inputs.Generation == 0 || c.Inputs.Generation != in.Current.Generation || c.Inputs.ClassifierVersion == "" {
		addFailure("classification basis is unresolved or stale")
		e.ReviewsKnown = false
	}
	if !c.InventoryComplete {
		pool := IdentitySet{Reason: "incomplete inventory: split the change to resolve policy owners"}
		obligations = append(obligations, obligation{"policy owners", Requirement{Selector: AnyWriter, Count: 1}, &pool})
	} else if c.label("policy") != False || len(c.PolicyOwners) > 0 {
		if !c.PolicyOwnersKnown || len(c.PolicyOwners) == 0 {
			pool := IdentitySet{Reason: "policy paths or effective owners are unresolved"}
			obligations = append(obligations, obligation{"policy owners", Requirement{Selector: AnyWriter, Count: 1}, &pool})
		}
	}
	for _, owner := range c.PolicyOwners {
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
	actors := make(map[string]Actor, len(in.Actors)+len(in.Stances))
	for id, actor := range in.Actors {
		actors[id] = actor
	}
	for id := range in.Stances {
		if _, ok := actors[id]; !ok {
			actors[id] = Actor{}
		}
	}
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
		for id, actor := range actors {
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
			validReceipt := stance.Receipt != nil && in.Current.recorded() && *stance.Receipt == in.Current
			// An invalid latest receipt never falls back to an older native review.
			if stance.Receipt != nil && !validReceipt {
				continue
			}
			approved[id] = true
			if !r.Receipt || validReceipt {
				result.ApprovedIDs = append(result.ApprovedIDs, id)
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
	if actor.Identity.ID != id || actor.Identity.Login == "" {
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
	if actor.Grant.permits(id) == False {
		return False
	}
	if pool != nil && (!pool.Resolved || !pool.Fresh) {
		return Unknown
	}
	return actor.Grant.permits(id)
}
