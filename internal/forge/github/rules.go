package github

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
)

const fencePrefix = "refs/heads/openmerge/fence/"
const queuePrefix = "refs/heads/openmerge/queue/"

var fencePatterns = []string{fencePrefix + "*", fencePrefix + "**/*"}
var fenceRuleTypes = []string{"creation", "update", "deletion", "non_fast_forward"}

// Ruleset retains rule parameters and conditions verbatim, including rules this
// version of OpenMerge does not understand. Never rebuild an existing policy
// from just the rules the queue knows about.
type Ruleset struct {
	ID           int64            `json:"id,omitempty"`
	Name         string           `json:"name"`
	Target       string           `json:"target"`
	SourceType   string           `json:"source_type,omitempty"`
	Source       string           `json:"source,omitempty"`
	Enforcement  string           `json:"enforcement"`
	BypassActors []BypassActor    `json:"bypass_actors"`
	Conditions   json.RawMessage  `json:"conditions"`
	Rules        []RepositoryRule `json:"rules"`
}

// BypassActor identifies a GitHub ruleset bypass, using the App ID, never an
// installation ID, for Integration actors.
type BypassActor struct {
	ActorID    *int64 `json:"actor_id"`
	ActorType  string `json:"actor_type"`
	BypassMode string `json:"bypass_mode"`
}

// RepositoryRule preserves all parameters of an existing rule.
type RepositoryRule struct {
	Type       string          `json:"type"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type refConditions struct {
	RefName struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	} `json:"ref_name"`
}

// RuleProblemCode is stable so a future server can fail startup by problem type.
type RuleProblemCode string

// Rule problem codes describe missing bypasses, fence ownership, blocked refs,
// and policy details that cannot be validated safely.
const (
	BaseBypassMissing RuleProblemCode = "base_bypass_missing"
	FenceUnprotected  RuleProblemCode = "fence_unprotected"
	RefBlocked        RuleProblemCode = "ref_blocked"
	RulesUnknown      RuleProblemCode = "rules_unknown"
)

// RuleProblem names the policy and the operator action needed to fix it.
type RuleProblem struct {
	Code      RuleProblemCode
	RulesetID int64
	Ruleset   string
	Ref       string
	Message   string
}

func (p RuleProblem) Error() string { return p.Message }

// ValidateRules is pure. Pass all detailed repository AND inherited rulesets,
// including bypass_actors (an admin read is needed to see these). Classic branch
// protection is separate and must also be checked by the caller. Unknown
// patterns fail closed; queue/fence overlaps are deliberately conservative.
func ValidateRules(rulesets []Ruleset, base string, appID int64) []RuleProblem {
	var problems []RuleProblem
	if appID <= 0 || base == "" {
		return []RuleProblem{{Code: RulesUnknown, Message: "supply a positive App ID and base branch before validating rules"}}
	}
	baseRef := "refs/heads/" + strings.TrimPrefix(base, "refs/heads/")
	protected := false
	for _, r := range rulesets {
		if r.Enforcement == "disabled" || r.Enforcement == "evaluate" || r.Target == "tag" {
			continue
		}
		problem := func(code RuleProblemCode, ref, message string) {
			problems = append(problems, RuleProblem{Code: code, RulesetID: r.ID, Ruleset: r.Name, Ref: ref, Message: fmt.Sprintf("ruleset %q (%d): %s", r.Name, r.ID, message)})
		}
		if r.Enforcement != "active" || r.Target != "branch" && r.Target != "push" {
			problem(RulesUnknown, "", "unknown target/enforcement; inspect this policy manually")
			continue
		}
		if r.Rules == nil {
			problem(RulesUnknown, "", "rule details are missing; fetch the complete ruleset before validating")
			continue
		}
		if r.BypassActors == nil {
			problem(RulesUnknown, "", "bypass actors are not visible; use a repository admin token")
			continue
		}
		var c refConditions
		if r.Target == "branch" && (json.Unmarshal(r.Conditions, &c) != nil || len(c.RefName.Include) == 0 || !validPatterns(c)) {
			problem(RulesUnknown, "", "missing or unsupported ref conditions; inspect this policy manually")
			continue
		}
		bypass := appBypass(r, appID)
		if len(r.Rules) > 0 && (r.Target == "push" || applies(c, baseRef, baseRef)) && !bypass {
			problem(BaseBypassMissing, baseRef, fmt.Sprintf("add Integration App %d with bypass_mode=always so the atomic base push can land", appID))
		}
		for _, prefix := range []string{fencePrefix, queuePrefix} {
			if len(r.Rules) > 0 && (r.Target == "push" || overlaps(c, prefix, baseRef)) && !bypass {
				problem(RefBlocked, prefix+"*", fmt.Sprintf("may block App creation/force-push in %s*; add the App bypass or exclude this namespace", prefix))
			}
		}
		if r.Target == "branch" && r.Enforcement == "active" && bypass && len(r.BypassActors) == 1 && covers(c, fencePrefix, baseRef) && hasFenceRules(r) {
			protected = true
		}
	}
	if !protected {
		problems = append(problems, RuleProblem{Code: FenceUnprotected, Ref: fencePrefix + "*", Message: "protect all openmerge/fence/* branches with active creation, update, deletion and non_fast_forward rules; only this App may bypass (always)"})
	}
	return problems
}

func appBypass(r Ruleset, id int64) bool {
	return slices.ContainsFunc(r.BypassActors, func(a BypassActor) bool {
		return a.ActorType == "Integration" && a.ActorID != nil && *a.ActorID == id && a.BypassMode == "always"
	})
}

func hasFenceRules(r Ruleset) bool {
	for _, kind := range fenceRuleTypes {
		if !slices.ContainsFunc(r.Rules, func(rule RepositoryRule) bool {
			return rule.Type == kind && (kind != "update" || strictFenceUpdate(rule))
		}) {
			return false
		}
	}
	return true
}

func strictFenceUpdate(rule RepositoryRule) bool {
	var params struct {
		Allow *bool `json:"update_allows_fetch_and_merge"`
	}
	return json.Unmarshal(rule.Parameters, &params) == nil && params.Allow != nil && !*params.Allow
}

func fenceRule(kind string) RepositoryRule {
	rule := RepositoryRule{Type: kind}
	if kind == "update" {
		rule.Parameters = json.RawMessage(`{"update_allows_fetch_and_merge":false}`)
	}
	return rule
}

func validPatterns(c refConditions) bool {
	for _, patterns := range [][]string{c.RefName.Include, c.RefName.Exclude} {
		for _, pattern := range patterns {
			if pattern != "~ALL" && pattern != "~DEFAULT_BRANCH" && !strings.HasPrefix(pattern, "refs/heads/") {
				return false
			}
			if _, err := path.Match(pattern, ""); err != nil || strings.ContainsAny(pattern, "\\{}") || strings.Contains(pattern, "[!") || strings.Contains(pattern, "[^") {
				return false
			}
		}
	}
	return true
}

// GitHub uses pathname-aware fnmatch: * cannot cross '/', **/ can.
func matches(pattern, ref, base string) bool {
	if pattern == "~ALL" {
		return true
	}
	if pattern == "~DEFAULT_BRANCH" {
		return ref == base
	}
	var match func([]string, []string) bool
	match = func(p, v []string) bool {
		if len(p) == 0 {
			return len(v) == 0
		}
		if p[0] == "**" && len(p) > 1 {
			return match(p[1:], v) || len(v) > 0 && match(p, v[1:])
		}
		if len(v) == 0 {
			return false
		}
		ok, _ := path.Match(p[0], v[0])
		return ok && match(p[1:], v[1:])
	}
	return match(strings.Split(pattern, "/"), strings.Split(ref, "/"))
}

func applies(c refConditions, ref, base string) bool {
	return slices.ContainsFunc(c.RefName.Include, func(p string) bool { return matches(p, ref, base) }) &&
		!slices.ContainsFunc(c.RefName.Exclude, func(p string) bool { return matches(p, ref, base) })
}

func patternCovers(pattern, prefix string) bool {
	if pattern == "~ALL" {
		return true
	}
	for parent := strings.TrimSuffix(prefix, "/"); strings.Contains(parent, "/"); parent = path.Dir(parent) {
		if pattern == parent+"/**/*" {
			return true
		}
	}
	return false
}

func patternOverlaps(pattern, prefix, base string) bool {
	if pattern == "~ALL" {
		return true
	}
	if pattern == "~DEFAULT_BRANCH" {
		return strings.HasPrefix(base, prefix)
	}
	literal := pattern
	if i := strings.IndexAny(literal, "*?["); i >= 0 {
		literal = literal[:i]
	}
	return strings.HasPrefix(literal, prefix) || strings.HasPrefix(prefix, literal)
}

func overlaps(c refConditions, prefix, base string) bool {
	return slices.ContainsFunc(c.RefName.Include, func(p string) bool { return patternOverlaps(p, prefix, base) }) &&
		!slices.ContainsFunc(c.RefName.Exclude, func(p string) bool { return patternCovers(p, prefix) })
}

func covers(c refConditions, prefix, base string) bool {
	return slices.ContainsFunc(c.RefName.Include, func(p string) bool { return patternCovers(p, prefix) }) &&
		!slices.ContainsFunc(c.RefName.Exclude, func(p string) bool { return patternOverlaps(p, prefix, base) })
}
