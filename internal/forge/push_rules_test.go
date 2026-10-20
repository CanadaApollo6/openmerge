package forge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func preparedFixture() PreparedPush {
	old, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	return PreparedPush{Operation: PushBuild, Complete: true, Refs: []PushRef{{Ref: "refs/heads/openmerge/queue/main/x", OldSHA: old, NewSHA: tip, Boundary: []string{old}, Commits: []PushCommit{{OID: tip, Parents: []string{old}, Message: "generated\n\nOpenMerge-Batch: x\n", Files: []PushFile{{Path: "src/main.go", OID: strings.Repeat("c", 40), Kind: "blob", Size: 10}}}}}}}
}

func ruleContext(rules ...authority.Rule) PushRuleContext {
	return PushRuleContext{AppID: "42", DefaultBranch: "main", Observation: authority.Observation{
		Inventory: authority.Visible, Findings: []authority.Finding{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/one"}}},
		Protections: []authority.Protection{{ID: "one", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible,
			Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: rules, BypassActors: []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}}}},
	}}
}

func TestPreparedPushCompleteness(t *testing.T) {
	if err := ValidatePreparedPush(preparedFixture()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*PreparedPush){
		"incomplete":     func(p *PreparedPush) { p.Complete = false },
		"missing tree":   func(p *PreparedPush) { p.Refs[0].Commits[0].Files = nil },
		"missing parent": func(p *PreparedPush) { p.Refs[0].Boundary = []string{} },
		"unreachable": func(p *PreparedPush) {
			p.Refs[0].Commits = append(p.Refs[0].Commits, PushCommit{OID: strings.Repeat("d", 40), Parents: []string{}, Files: []PushFile{}})
		},
		"duplicate":                 func(p *PreparedPush) { p.Refs = append(p.Refs, p.Refs[0]) },
		"signature metadata budget": func(p *PreparedPush) { p.Refs[0].Commits[0].Signature = strings.Repeat("x", MaxPushMetadataBytes+1) },
		"atomic missing fence":      func(p *PreparedPush) { p.Operation = PushLand },
		"deletion with content":     func(p *PreparedPush) { p.Operation = PushDeleteBranch; p.Refs[0].NewSHA = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := preparedFixture()
			change(&p)
			if err := ValidatePreparedPush(p); err == nil {
				t.Fatal("incomplete evidence accepted")
			}
		})
	}
}

func TestOperationRuleMatrix(t *testing.T) {
	msg := func(op, pattern string) []authority.Parameter {
		a, _ := json.Marshal(op)
		b, _ := json.Marshal(pattern)
		return []authority.Parameter{{Name: "operator", Values: []string{string(a)}}, {Name: "pattern", Values: []string{string(b)}}}
	}
	for _, tc := range []struct {
		name, kind   string
		params       []authority.Parameter
		reject, warn bool
	}{
		{name: "linear", kind: "required_linear_history", params: []authority.Parameter{}},
		{name: "unsigned bypass still rejects", kind: "required_signatures", params: []authority.Parameter{}, reject: true},
		{name: "unknown bypass still rejects", kind: "future_rule", params: []authority.Parameter{}, reject: true},
		{name: "message actual trailers", kind: "commit_message_pattern", params: msg("contains", "OpenMerge-Batch: x")},
		{name: "message rejection", kind: "commit_message_pattern", params: msg("contains", "absent"), reject: true},
		{name: "regex unsupported", kind: "commit_message_pattern", params: msg("regex", ".*"), reject: true},
		{name: "size", kind: "file_size_restriction", params: []authority.Parameter{{Name: "max_file_size", Values: []string{"1"}}}},
		{name: "path", kind: "file_path_restriction", params: []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"src/**"`}}}, reject: true},
		{name: "PR bypass", kind: "pull_request", params: []authority.Parameter{}, warn: true},
		{name: "unknown parameter", kind: "required_linear_history", params: []authority.Parameter{{Name: "extra", Values: []string{"true"}}}, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := preparedFixture()
			warnings, err := EvaluatePushRules(p, ruleContext(authority.Rule{Kind: tc.kind, Parameters: tc.params}))
			if (err != nil) != tc.reject || (len(warnings) > 0) != tc.warn {
				t.Fatalf("warnings %v error %v", warnings, err)
			}
		})
	}
	// A bypass in one ruleset cannot waive another's review requirement.
	ctx := ruleContext(authority.Rule{Kind: "pull_request", Parameters: []authority.Parameter{}})
	other := ctx.Observation.Protections[0]
	other.ID = "two"
	other.BypassActors = []authority.BypassActor{}
	ctx.Observation.Protections = append(ctx.Observation.Protections, other)
	if _, err := EvaluatePushRules(preparedFixture(), ctx); err == nil {
		t.Fatal("borrowed another policy's bypass")
	}
}

func TestRulesInspectIntermediateParentsAndBothAtomicRefs(t *testing.T) {
	p := preparedFixture()
	r := &p.Refs[0]
	c := r.Commits[0]
	c.OID = strings.Repeat("d", 40)
	c.Message = "disallowed intermediate"
	r.Commits[0].Parents = []string{c.OID}
	r.Commits = append(r.Commits, c)
	rule := authority.Rule{Kind: "commit_message_pattern", Parameters: []authority.Parameter{{Name: "operator", Values: []string{`"contains"`}}, {Name: "pattern", Values: []string{`"OpenMerge-Batch"`}}}}
	if _, err := EvaluatePushRules(p, ruleContext(rule)); err == nil {
		t.Fatal("skipped intermediate commit")
	}
	p = preparedFixture()
	p.Operation = PushLand
	fence := p.Refs[0]
	fence.Ref = "refs/heads/openmerge/fence/main"
	fence.Commits = append([]PushCommit(nil), fence.Commits...)
	fence.Commits[0].Parents = append([]string(nil), fence.Commits[0].Parents...)
	fence.Commits[0].Message = "fence"
	p.Refs = append(p.Refs, fence)
	if _, err := EvaluatePushRules(p, ruleContext(rule)); err == nil {
		t.Fatal("skipped atomic fence")
	}
	p = preparedFixture()
	p.Refs[0].Commits[0].Parents = append(p.Refs[0].Commits[0].Parents, p.Refs[0].OldSHA)
	if _, err := EvaluatePushRules(p, ruleContext(authority.Rule{Kind: "required_linear_history", Parameters: []authority.Parameter{}})); err == nil {
		t.Fatal("accepted contributor merge")
	}
}

func TestDeleteRulesAndIntent(t *testing.T) {
	p := PreparedPush{Operation: PushDeleteBranch, Complete: true, Refs: []PushRef{{Ref: "refs/heads/topic", OldSHA: strings.Repeat("a", 40), Commits: []PushCommit{}, Boundary: []string{}}}}
	ctx := ruleContext(authority.Rule{Kind: "deletion", Parameters: []authority.Parameter{}})
	ctx.Observation.Protections[0].BypassActors = []authority.BypassActor{}
	if _, err := EvaluatePushRules(p, ctx); err == nil {
		t.Fatal("accepted restricted deletion")
	}
	if !MatchesPreparedRefs([]PushRefIntent{{Ref: p.Refs[0].Ref, OldSHA: p.Refs[0].OldSHA, Delete: true}}, p.Refs) {
		t.Fatal("deletion intent mismatch")
	}
	if MatchesPreparedRefs([]PushRefIntent{{Ref: p.Refs[0].Ref, OldSHA: p.Refs[0].OldSHA}}, p.Refs) {
		t.Fatal("unbound deletion")
	}
}
