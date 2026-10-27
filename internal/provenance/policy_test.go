package provenance

import (
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func TestNoNetworkPolicyValidationAndIdentity(t *testing.T) {
	baseline := cp.Policy{Digest: "baseline", Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}}, Groups: map[string]cp.IdentitySet{"security": {}}}
	rule := Rule{When: expression(t, "#agent-commits > 0"), Approvals: &cp.Requirement{Selector: cp.Human, Count: 1}}
	parser := NewParserPolicy("git version 2.56.0")
	limits := DefaultInventoryLimits()
	makePolicy := func(base cp.Policy, rules []Rule, ids []IdentityRule, parser ParserPolicy, limits InventoryLimits) (*Policy, error) {
		return NewPolicy(base, rules, ids, parser, limits)
	}
	original, err := makePolicy(baseline, []Rule{rule}, nil, parser, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*cp.Policy, *Rule, *[]IdentityRule, *ParserPolicy, *InventoryLimits){
		func(b *cp.Policy, _ *Rule, _ *[]IdentityRule, _ *ParserPolicy, _ *InventoryLimits) {
			b.Digest = "edited baseline"
		},
		func(_ *cp.Policy, r *Rule, _ *[]IdentityRule, _ *ParserPolicy, _ *InventoryLimits) {
			r.When = expression(t, "provenance = undeclared")
		},
		func(_ *cp.Policy, r *Rule, _ *[]IdentityRule, _ *ParserPolicy, _ *InventoryLimits) {
			a := *r.Approvals
			a.Count++
			r.Approvals = &a
		},
		func(_ *cp.Policy, _ *Rule, ids *[]IdentityRule, _ *ParserPolicy, _ *InventoryLimits) {
			*ids = []IdentityRule{{ID: "human", Mailbox: "human@example.test", Class: Human}}
		},
		func(_ *cp.Policy, _ *Rule, _ *[]IdentityRule, p *ParserPolicy, _ *InventoryLimits) {
			*p = NewParserPolicy("git version 2.55.0")
		},
		func(_ *cp.Policy, _ *Rule, _ *[]IdentityRule, _ *ParserPolicy, l *InventoryLimits) { l.SourceCommits-- },
	} {
		base, r, ids, parser, limits := baseline, rule, []IdentityRule{}, parser, limits
		mutate(&base, &r, &ids, &parser, &limits)
		changed, err := makePolicy(base, []Rule{r}, ids, parser, limits)
		if err != nil || changed.Digest() == original.Digest() {
			t.Fatal("policy edit not bound", err)
		}
	}
	// False rules are still policy inputs, even when absent from a particular
	// revision's narrower declaration digest.
	falseRule := rule
	falseRule.When = condition.Any()
	changed, err := makePolicy(baseline, []Rule{rule, falseRule}, nil, parser, limits)
	if err != nil || changed.Digest() == original.Digest() {
		t.Fatal("false rule dropped", err)
	}
	for _, attribute := range []string{"checkpoint-present", "#unresolved-checkpoints = 0"} {
		e := expression(t, attribute)
		for _, trigger := range []bool{false, true} {
			r := rule
			r.Conditions = &e
			if trigger {
				r.When = e
				r.Conditions = nil
			}
			if _, err := makePolicy(baseline, []Rule{r}, nil, parser, limits); err == nil {
				t.Fatal("resolver activated")
			}
		}
	}
	for _, r := range []Rule{{When: condition.All()}, {When: condition.All(), Approvals: &cp.Requirement{Selector: cp.None}}, {When: condition.All(), Approvals: &cp.Requirement{Selector: cp.Group, Group: "missing", Count: 1}}} {
		if _, err := makePolicy(baseline, []Rule{r}, nil, parser, limits); err == nil {
			t.Fatal("invalid additive requirement")
		}
	}
	noApprovals := baseline
	noApprovals.Approvals = nil
	if _, err := makePolicy(noApprovals, []Rule{rule}, nil, parser, limits); err == nil {
		t.Fatal("missing approvals opt-in")
	}
	withOrdinary, err := NewPolicy(baseline, []Rule{rule}, nil, parser, limits, condition.Not(condition.All()))
	if err != nil || withOrdinary.Digest() == original.Digest() {
		t.Fatal("ordinary policy expressions not bound", err)
	}
	// Definitions are immutable across caller edits and returned copies.
	before := original.Rules()[0].Approvals.Count
	rule.Approvals.Count = 99
	returned := original.Rules()
	returned[0].Approvals.Count = 42
	if original.Rules()[0].Approvals.Count != before {
		t.Fatal("policy aliased caller")
	}
}
