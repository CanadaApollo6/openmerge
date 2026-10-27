package provenance

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

var classComparison = regexp.MustCompile(`^-?provenance (?:=|!=) "(?:agent|human|mixed|undeclared)"$`)

// ParseCondition reuses the existing typed grammar and Kleene evaluator while
// leaving public parsing and frozen evidence validation unchanged.
func ParseCondition(text string) (condition.Expression, error) {
	e, err := condition.ParseCandidate(text, condition.Snapshot{
		"provenance": condition.String(""), "agent-commits": condition.Strings(),
		"unresolved-checkpoints": condition.Strings(), "checkpoint-present": condition.Bool(false),
	})
	if err != nil {
		return e, err
	}
	if slices.Contains(e.Attributes(), "provenance") && !classComparison.MatchString(e.String()) {
		return condition.Expression{}, fmt.Errorf("provenance requires exact = or != and agent, human, mixed or undeclared")
	}
	return e, nil
}

// DeclarationAttributes returns only immutable declaration-derived knowledge.
// Resolver attributes are deliberately absent, even with no checkpoints, so a
// digest cannot drop a rule based on resolution observations. Incomplete input
// yields unknown for every declaration attribute, including counts under NOT.
func DeclarationAttributes(commits []Commit, complete bool) condition.Snapshot {
	result := condition.Snapshot{}
	if !complete || slices.ContainsFunc(commits, func(c Commit) bool { return !c.Complete }) {
		return result
	}
	agent, human, allHuman := false, false, len(commits) > 0
	agentIDs := []string{}
	for _, c := range commits {
		class := Classify(c.Declarations)
		a, h := class == Agent || class == Mixed, class == Human || class == Mixed
		agent = agent || a
		human = human || h
		allHuman = allHuman && h
		if a {
			agentIDs = append(agentIDs, c.ObjectID)
		}
	}
	class := Undeclared
	switch {
	case agent && human:
		class = Mixed
	case agent:
		class = Agent
	case allHuman:
		class = Human
	}
	slices.Sort(agentIDs)
	agentIDs = slices.Compact(agentIDs)
	result["provenance"] = condition.String(string(class))
	result["agent-commits"] = condition.Strings(agentIDs...)
	return result
}

// Truth evaluates with the repository's existing three-valued types. Unknown
// never passes under negation. Evaluating the complement distinguishes a known
// false from unknown without inferring group truth from individual failures.
func Truth(e condition.Expression, inputs condition.PR) cp.Truth {
	if ok, _ := e.Evaluate(inputs); ok {
		return cp.True
	}
	if ok, _ := condition.Not(e).Evaluate(inputs); ok {
		return cp.False
	}
	return cp.Unknown
}

func conjunction(a, b cp.Truth) cp.Truth {
	if a == cp.False || b == cp.False {
		return cp.False
	}
	if a != cp.True || b != cp.True {
		return cp.Unknown
	}
	return cp.True
}

// Rule only adds independent conditions and/or an ADR 0007 approval obligation.
type Rule struct {
	When       condition.Expression
	Conditions *condition.Expression
	Approvals  *cp.Requirement
}

// Validate checks the value contract independently of candidate YAML parsing.
func (r Rule) Validate(approvalsEnabled bool, groups map[string][]string) error {
	if r.Conditions == nil && r.Approvals == nil {
		return fmt.Errorf("require needs conditions and/or approvals")
	}
	if r.Approvals != nil {
		if !approvalsEnabled {
			return fmt.Errorf("provenance approvals require the queue approvals opt-in")
		}
		if err := r.Approvals.Validate(); err != nil {
			return err
		}
		if r.Approvals.Selector == cp.None {
			return fmt.Errorf("provenance cannot require none")
		}
		if r.Approvals.Selector == cp.Group {
			if _, ok := groups[r.Approvals.Group]; !ok {
				return fmt.Errorf("unknown group %q", r.Approvals.Group)
			}
		}
	}
	return nil
}

// RuleResult retains independent truth values and evaluator inputs. This is a
// pure candidate DTO, not a journal/wire evidence contract.
type RuleResult struct {
	Trigger, Obligation, Result cp.Truth
	WhenInputs                  condition.Evidence
	ConditionInputs             condition.Evidence
}

// Evaluation keeps the unchanged baseline separate from additive obligations.
type Evaluation struct {
	Baseline, Result cp.Truth
	Rules            []RuleResult
}

// EvaluateRequirements consumes already evaluated independent approval pools.
// local[i] must describe only rule i's approval obligation, with ADR 0007's
// exclusions and vetoes applied. It never contributes to baseline or triggers.
// With no approvals opt-in, the caller's legacy review attribute is preserved.
func EvaluateRequirements(baselineExpression condition.Expression, baseline condition.Snapshot, baselineApprovals cp.Evaluation, declarations condition.Snapshot, rules []Rule, local []cp.Evaluation) Evaluation {
	base := maps.Clone(baseline)
	if base == nil {
		base = condition.Snapshot{}
	}
	if baselineApprovals.OptedIn {
		base["approved-reviews-by"] = baselineApprovals.ReviewAttribute()
	}
	b := Truth(baselineExpression, base)
	if baselineApprovals.OptedIn {
		b = conjunction(b, approvalTruth(baselineApprovals))
	}
	out := Evaluation{Baseline: b, Result: b}
	triggers := maps.Clone(base)
	for _, name := range []string{"provenance", "agent-commits", "unresolved-checkpoints", "checkpoint-present"} {
		triggers[name] = declarations[name]
	}
	for i, r := range rules {
		trigger := Truth(r.When, triggers)
		inputs := maps.Clone(triggers)
		obligation := cp.True
		if r.Approvals != nil {
			pool := cp.Evaluation{}
			if i < len(local) {
				pool = local[i]
			}
			obligation = approvalTruth(pool)
			// Unknown baseline membership cannot become a known expanded set.
			if baselineApprovals.OptedIn && baselineApprovals.ReviewsKnown && pool.OptedIn && pool.ReviewsKnown {
				names := append(slices.Clone(baselineApprovals.ReviewLogins), pool.ReviewLogins...)
				slices.Sort(names)
				inputs["approved-reviews-by"] = condition.Strings(slices.Compact(names)...)
			} else {
				inputs["approved-reviews-by"] = condition.Value{}
			}
		}
		rr := RuleResult{Trigger: trigger, WhenInputs: r.When.Record(triggers)}
		if r.Conditions != nil {
			obligation = conjunction(obligation, Truth(*r.Conditions, inputs))
			rr.ConditionInputs = r.Conditions.Record(inputs)
		}
		rr.Obligation = obligation
		switch {
		case trigger == cp.False || obligation == cp.True:
			rr.Result = cp.True
		case trigger == cp.True:
			rr.Result = obligation
		default:
			rr.Result = cp.Unknown
		}
		out.Result = conjunction(out.Result, rr.Result)
		out.Rules = append(out.Rules, rr)
	}
	return out
}

func approvalTruth(e cp.Evaluation) cp.Truth {
	if !e.OptedIn {
		return cp.Unknown
	}
	if e.Allowed {
		return cp.True
	}
	// A known unmet obligation or veto is false; unresolved pools remain pending.
	if len(e.BlockingIDs) > 0 {
		return cp.False
	}
	for _, r := range e.Requirements {
		if !r.Met && !r.Unresolved {
			return cp.False
		}
	}
	if !e.ReviewsKnown {
		return cp.Unknown
	}
	return cp.False
}
