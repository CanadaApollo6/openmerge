package forge

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// PushRuleContext holds the original complete protection inventory. Bypass is
// determined separately for each applicable policy, never inferred globally.
type PushRuleContext struct {
	DefaultBranch string
	AppID         string
	Observation   authority.Observation
}

// PushWarning identifies a PR policy actually bypassed by this App. Content
// policy is always validated, including while the App retains a bypass.
type PushWarning struct{ ProtectionID, Ref, Rule string }

// EvaluatePushRules is pure. Every active repository/inherited/push/classic
// policy is evaluated for every affected ref. Unknown parameters and patterns
// fail closed; unsupported families remain explicitly incompatible.
func EvaluatePushRules(p PreparedPush, ctx PushRuleContext) ([]PushWarning, error) {
	if err := ValidatePreparedPush(p); err != nil {
		return nil, err
	}
	if ctx.AppID == "" || ctx.DefaultBranch == "" || !ctx.Observation.SeeingComplete() {
		return nil, pushRejected("complete protection evidence required")
	}
	warnings := []PushWarning{}
	for _, policy := range ctx.Observation.Protections {
		if policy.Enforcement != "active" {
			continue
		}
		if policy.Target == "tag" {
			continue
		}
		if policy.Target != "branch" && policy.Target != "push" && policy.Target != "repository" {
			return nil, pushRejected("unsupported protection target")
		}
		// Repository selectors have already been resolved by the adapter's
		// applicable-policy read. They cannot alter per-ref matching here.
		bypass := false
		for _, actor := range policy.BypassActors {
			if actor.Kind == "app" && actor.ID == ctx.AppID && actor.Mode == "always" {
				bypass = true
			}
		}
		for _, ref := range p.Refs {
			applies := policy.Target != "branch"
			if policy.Target == "branch" {
				var err error
				applies, err = (authority.RefCoverage{Includes: policy.Includes, Excludes: policy.Excludes}).ContainsRef(ref.Ref, "refs/heads/"+ctx.DefaultBranch)
				if err != nil || len(policy.Includes) == 0 {
					return nil, pushRejected("unsupported or missing ref conditions")
				}
			}
			if !applies {
				continue
			}
			for _, rule := range policy.Rules {
				warn, err := evaluatePushRule(rule, ref, bypass)
				if err != nil {
					return nil, fmt.Errorf("protection %s ref %s rule %s: %w", policy.ID, ref.Ref, rule.Kind, err)
				}
				if warn {
					warnings = append(warnings, PushWarning{policy.ID, ref.Ref, rule.Kind})
				}
			}
		}
	}
	return warnings, nil
}

func evaluatePushRule(rule authority.Rule, ref PushRef, bypass bool) (bool, error) {
	// These rules concern admission/reviews/check counting, which stays with
	// GitHub unless this exact policy permits the App's direct push bypass.
	switch rule.Kind {
	case "pull_request", "required_status_checks", "required_deployments", "required_workflows", "code_scanning":
		if ref.NewSHA == "" {
			return false, nil
		}
		if !bypass {
			return false, pushRejected("PR policy is not bypassed by this App")
		}
		return true, nil
	}
	params := map[string][]string{}
	for _, p := range rule.Parameters {
		if _, ok := params[p.Name]; ok {
			return false, pushRejected("duplicate rule parameter")
		}
		params[p.Name] = p.Values
	}
	switch rule.Kind {
	case "creation", "deletion", "non_fast_forward", "required_linear_history", "required_signatures":
		if len(params) != 0 {
			return false, pushRejected("unsupported rule parameters")
		}
	case "update":
		for key, v := range params {
			if key != "update_allows_fetch_and_merge" || len(v) != 1 || v[0] != "false" && v[0] != "true" {
				return false, pushRejected("unsupported update parameters")
			}
		}
	}
	switch rule.Kind {
	case "creation":
		if ref.OldSHA == "" && !bypass {
			return false, pushRejected("ref creation restricted")
		}
	case "update":
		if ref.OldSHA != "" && ref.NewSHA != "" && !bypass {
			return false, pushRejected("direct ref update restricted")
		}
	case "deletion":
		if ref.NewSHA == "" && !bypass {
			return false, pushRejected("ref deletion restricted")
		}
	case "non_fast_forward":
		if ref.OldSHA != "" && ref.NewSHA != "" && ref.OldSHA != ref.NewSHA {
			ancestor := false
			for _, oid := range ref.Boundary {
				if oid == ref.OldSHA {
					ancestor = true
				}
			}
			if !ancestor && !bypass {
				return false, pushRejected("non fast-forward update")
			}
		}
	case "required_linear_history":
		for _, c := range ref.Commits {
			if len(c.Parents) > 1 {
				return false, pushRejected("merge commit violates linear history")
			}
		}
	case "required_signatures":
		for _, c := range ref.Commits {
			if c.Signature == "" || !c.SignatureVerified {
				return false, pushRejected("commit lacks verified signature")
			}
		}
	case "commit_message_pattern":
		for _, c := range ref.Commits {
			if err := validateMessage(params, c.Message); err != nil {
				return false, err
			}
		}
		// Validate parameter semantics even for a deletion or an empty graph.
		if err := messageParameters(params); err != nil {
			return false, err
		}
	case "file_size_restriction":
		raw, ok := singleParameter(params, "max_file_size")
		if !ok || len(params) != 1 {
			return false, pushRejected("unsupported size parameters")
		}
		limit, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || limit < 1 || limit > 1<<40 {
			return false, pushRejected("invalid size limit")
		}
		for _, c := range ref.Commits {
			for _, f := range c.Files {
				if f.Kind == "blob" && f.Size > limit*1024*1024 {
					return false, pushRejected("blob exceeds size limit")
				}
			}
		}
	case "file_path_restriction":
		patterns, err := arrayParameters(params, "restricted_file_paths")
		if err != nil {
			return false, err
		}
		for _, pattern := range patterns {
			if _, err := authority.MatchRefPattern(pattern, "", ""); err != nil {
				return false, pushRejected("unsupported path pattern")
			}
			for _, c := range ref.Commits {
				for _, f := range c.Files {
					match, err := authority.MatchRefPattern(pattern, f.Path, "")
					if err != nil || match {
						return false, pushRejected("restricted file path")
					}
				}
			}
		}
	default:
		return false, pushRejected("unsupported rule")
	}
	return false, nil
}

func singleParameter(params map[string][]string, key string) (string, bool) {
	v, ok := params[key]
	return func() (string, bool) {
		if !ok || len(v) != 1 {
			return "", false
		}
		return v[0], true
	}()
}

func messageParameters(params map[string][]string) error {
	for key := range params {
		if key != "name" && key != "operator" && key != "pattern" && key != "negate" {
			return pushRejected("unsupported message parameters")
		}
	}
	op, ok := singleParameter(params, "operator")
	if !ok {
		return pushRejected("missing message operator")
	}
	var operator string
	if json.Unmarshal([]byte(op), &operator) != nil {
		return pushRejected("invalid message operator")
	}
	switch operator {
	case "starts_with", "ends_with", "contains":
	default:
		return pushRejected("unsupported message pattern semantics")
	}
	raw, ok := singleParameter(params, "pattern")
	var pattern string
	if !ok || json.Unmarshal([]byte(raw), &pattern) != nil {
		return pushRejected("invalid message pattern")
	}
	if raw, ok := params["negate"]; ok && (len(raw) != 1 || raw[0] != "true" && raw[0] != "false") {
		return pushRejected("invalid message negation")
	}
	if raw, ok := params["name"]; ok {
		var name string
		if len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &name) != nil {
			return pushRejected("invalid message name")
		}
	}
	return nil
}

func validateMessage(params map[string][]string, message string) error {
	if err := messageParameters(params); err != nil {
		return err
	}
	var operator, pattern string
	_ = json.Unmarshal([]byte(params["operator"][0]), &operator)
	_ = json.Unmarshal([]byte(params["pattern"][0]), &pattern)
	match := false
	switch operator {
	case "starts_with":
		match = strings.HasPrefix(message, pattern)
	case "ends_with":
		match = strings.HasSuffix(message, pattern)
	case "contains":
		match = strings.Contains(message, pattern)
	}
	if raw, ok := params["negate"]; ok && raw[0] == "true" {
		match = !match
	}
	if !match {
		return pushRejected("commit message rejected")
	}
	return nil
}

func arrayParameters(params map[string][]string, key string) ([]string, error) {
	if len(params) == 1 {
		if v, ok := params[key]; ok && len(v) == 0 {
			return []string{}, nil
		}
	}
	result := []string{}
	for i := 0; i < len(params); i++ {
		raw, ok := singleParameter(params, fmt.Sprintf("%s/~2%d", key, i))
		var value string
		if !ok || json.Unmarshal([]byte(raw), &value) != nil {
			return nil, pushRejected("unsupported array parameters")
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, pushRejected("missing array parameters")
	}
	return result, nil
}
