package forge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func preparedFixture() PreparedPush {
	old, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	return PreparedPush{Operation: PushBuild, Complete: true, Refs: []PushRef{{Ref: "refs/heads/openmerge/queue/main/x", OldSHA: old, NewSHA: tip, Boundary: []string{old}, Commits: []PushCommit{{OID: tip, Parents: []string{old}, Message: "generated\n\nOpenMerge-Batch: x\n", Diffs: []PushDiff{{Parent: old, Paths: []string{"src/main.go"}}}, Files: []PushFile{{Path: "src/main.go", OID: strings.Repeat("c", 40), Kind: "blob", Size: 10}}}}}}}
}

func ruleContext(rules ...authority.Rule) PushRuleContext {
	return PushRuleContext{AppID: "42", DefaultBranch: "main", BaseRef: "refs/heads/main", Observation: authority.Observation{
		Inventory: authority.Visible, Findings: []authority.Finding{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/one"}}},
		Protections: []authority.Protection{{ID: "one", Source: "repository_ruleset", Target: "push", Enforcement: "active", TargetsVisibility: authority.Visible, RulesVisibility: authority.Visible, BypassVisibility: authority.Visible,
			Includes: []string{}, Excludes: []string{}, Conditions: []authority.Parameter{}, Rules: append([]authority.Rule{}, rules...), BypassActors: []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}}}},
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
		{name: "size", kind: "max_file_size", params: []authority.Parameter{{Name: "max_file_size", Values: []string{"1"}}}},
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
	r.Commits[0].Diffs = []PushDiff{{Parent: c.OID, Paths: []string{"src/main.go"}}}
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
	p.Refs[0].Commits[0].Diffs = append(p.Refs[0].Commits[0].Diffs, p.Refs[0].Commits[0].Diffs[0])
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

func TestChangedPathRules(t *testing.T) {
	for _, path := range []string{"secret.pem", "~ALL", "~DEFAULT_BRANCH"} {
		for _, deletion := range []bool{false, true} {
			p := preparedFixture()
			c := &p.Refs[0].Commits[0]
			c.Diffs[0].Paths = []string{path, "destination"} // rename source is retained
			c.Files = []PushFile{}
			if !deletion {
				c.Files = []PushFile{{Path: "destination", OID: strings.Repeat("c", 40), Kind: "blob"}}
			}
			rule := authority.Rule{Kind: "file_path_restriction", Parameters: []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"` + path + `"`}}}}
			if _, err := EvaluatePushRules(p, ruleContext(rule)); err == nil {
				t.Fatalf("accepted changed path %q deletion=%v", path, deletion)
			}
			c.Diffs[0].Paths = []string{"unrelated"}
			c.Files = []PushFile{}
			if _, err := EvaluatePushRules(p, ruleContext(rule)); err != nil {
				t.Fatalf("unchanged restricted path: %v", err)
			}
		}
	}
	for _, rule := range []authority.Rule{
		{Kind: "file_extension_restriction", Parameters: []authority.Parameter{{Name: "restricted_file_extensions/~20", Values: []string{`".pem"`}}}},
		{Kind: "max_file_path_length", Parameters: []authority.Parameter{{Name: "max_file_path_length", Values: []string{"5"}}}},
	} {
		p := preparedFixture()
		p.Refs[0].Commits[0].Files = []PushFile{}
		p.Refs[0].Commits[0].Diffs[0].Paths = []string{"secret.pem"}
		if _, err := EvaluatePushRules(p, ruleContext(rule)); err == nil {
			t.Fatalf("accepted deletion under %s", rule.Kind)
		}
	}
	// A merge's change relative to its second parent cannot disappear in a
	// first-parent or combined diff. Every parent comparison is mandatory.
	p := preparedFixture()
	c := &p.Refs[0].Commits[0]
	c.Parents = append(c.Parents, p.Refs[0].OldSHA)
	c.Diffs = append(c.Diffs, PushDiff{Parent: p.Refs[0].OldSHA, Paths: []string{"secret.pem"}})
	rule := authority.Rule{Kind: "file_path_restriction", Parameters: []authority.Parameter{{Name: "restricted_file_paths/~20", Values: []string{`"secret.pem"`}}}}
	if _, err := EvaluatePushRules(p, ruleContext(rule)); err == nil {
		t.Fatal("skipped second parent")
	}
	c.Diffs = c.Diffs[:1]
	if err := ValidatePreparedPush(p); err == nil {
		t.Fatal("accepted missing merge comparison")
	}
}

func TestPreparedOperationShapesAndCreationProof(t *testing.T) {
	for _, operation := range []PushOperation{PushBuild, PushDeleteBranch, PushEnsureFence, PushAdvanceFence} {
		p := preparedFixture()
		p.Operation = operation
		switch operation {
		case PushBuild:
			p.Refs[0].NewSHA = ""
			p.Refs[0].Commits = []PushCommit{}
			p.Refs[0].Boundary = []string{}
		case PushAdvanceFence:
			p.Refs[0].OldSHA = ""
		}
		if err := ValidatePreparedPush(p); err == nil {
			t.Fatalf("accepted malformed %s", operation)
		}
	}
	p := preparedFixture()
	r := &p.Refs[0]
	r.OldSHA = ""
	if err := ValidatePreparedPush(p); err == nil {
		t.Fatal("unproven creation boundary")
	}
	r.CreationBaseSHA = r.Boundary[0]
	if err := ValidatePreparedPush(p); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedAggregateBudgets(t *testing.T) {
	for _, tc := range []struct {
		name string
		cap  int
		set  func(*PreparedPush, int)
	}{
		{"edges", MaxPushParentEdges, func(p *PreparedPush, n int) { p.Refs[0].Commits[0].Parents = make([]string, n) }},
		{"frontier", MaxPushBoundaryEntries, func(p *PreparedPush, n int) { p.Refs[0].Boundary = make([]string, n) }},
	} {
		for _, extra := range []int{0, 1} {
			p := preparedFixture()
			tc.set(&p, tc.cap+extra)
			if err := preparedBudgets(p); (err != nil) != (extra == 1) {
				t.Fatalf("%s cap+%d: %v", tc.name, extra, err)
			}
		}
	}
	p := preparedFixture()
	c := &p.Refs[0].Commits[0]
	if err := preparedBudgets(p); err != nil {
		t.Fatal(err)
	}
	// Count the reference, commit, parent, boundary, diff-parent and blob OIDs.
	base := len(p.Refs[0].Ref) + len(p.Refs[0].OldSHA) + len(p.Refs[0].NewSHA) + len(c.OID) + len(c.Parents[0]) + len(p.Refs[0].Boundary[0]) + len(c.Diffs[0].Parent) + len(c.Diffs[0].Paths[0]) + len(c.Files[0].Path) + len(c.Files[0].OID) + len(c.Files[0].Kind)
	c.Message = strings.Repeat("x", MaxPushMetadataBytes-base)
	if err := preparedBudgets(p); err != nil {
		t.Fatal(err)
	}
	c.Message += "x"
	if err := preparedBudgets(p); err == nil {
		t.Fatal("OID metadata not counted")
	}
}

func TestUnknownActorCannotBypass(t *testing.T) {
	ctx := ruleContext(authority.Rule{Kind: "pull_request", Parameters: []authority.Parameter{}})
	ctx.Observation.Protections[0].BypassActors = []authority.BypassActor{{Kind: "unknown", RawKind: "FutureIntegration", ID: "42", Mode: "always"}}
	if _, err := EvaluatePushRules(preparedFixture(), ctx); err == nil {
		t.Fatal("unknown actor satisfied App bypass")
	}
}

func TestPreparedGraphAtAggregateCaps(t *testing.T) {
	p := preparedFixture()
	r := &p.Refs[0]
	c := &r.Commits[0]
	c.Files = []PushFile{}
	c.Parents = []string{}
	c.Diffs = []PushDiff{}
	r.Boundary = []string{}
	for i := 1; i <= MaxPushParentEdges; i++ {
		oid := fmt.Sprintf("%040x", i)
		c.Parents = append(c.Parents, oid)
		r.Boundary = append(r.Boundary, oid)
		c.Diffs = append(c.Diffs, PushDiff{Parent: oid, Paths: []string{}})
	}
	if err := ValidatePreparedPush(p); err != nil {
		t.Fatalf("at cap: %v", err)
	}
	c.Parents = append(c.Parents, r.OldSHA)
	c.Diffs = append(c.Diffs, PushDiff{Parent: r.OldSHA, Paths: []string{}})
	if err := ValidatePreparedPush(p); err == nil {
		t.Fatal("cap+1 parent edges accepted")
	}
	c.Parents = c.Parents[:MaxPushParentEdges]
	c.Diffs = c.Diffs[:MaxPushParentEdges]
	r.Boundary = append(r.Boundary, r.OldSHA)
	if err := ValidatePreparedPush(p); err == nil {
		t.Fatal("cap+1 frontier entries accepted")
	}
}

func TestBuildChecksRetainedBaseContentPolicy(t *testing.T) {
	p := preparedFixture()
	ctx := ruleContext(authority.Rule{Kind: "required_signatures", Parameters: []authority.Parameter{}})
	ctx.Observation.Protections[0].Target = "branch"
	ctx.Observation.Protections[0].Includes = []string{"refs/heads/main"}
	if _, err := EvaluatePushRules(p, ctx); err == nil {
		t.Fatal("base-only content policy did not reject generated marker")
	}
}

func TestPreparedPushBoundsTotalParentEdges(t *testing.T) {
	p := preparedFixture()
	r := &p.Refs[0]
	r.Commits = []PushCommit{}
	parent := r.OldSHA
	for i := 0; i < 11; i++ {
		oid := fmt.Sprintf("%040x", i+1)
		parents := make([]string, 10_000)
		for j := range parents {
			parents[j] = parent
		}
		r.Commits = append(r.Commits, PushCommit{OID: oid, Parents: parents, Files: []PushFile{}})
		parent = oid
	}
	r.NewSHA = parent
	if err := ValidatePreparedPush(p); err == nil {
		t.Fatal("unbounded aggregate parent graph accepted")
	}
}
