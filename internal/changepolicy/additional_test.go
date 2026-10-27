package changepolicy

import "testing"

func TestAdditionalApprovalDoesNotImportBaselineObligations(t *testing.T) {
	in := currentInput()
	addReviewer(&in, "security", HumanAccount, false)
	// An unresolved policy-owner obligation belongs to the baseline. It must
	// not import owners into the security rule's condition reviewer snapshot.
	in.Classification.Facts["policy"] = Unknown
	groups := map[string]IdentitySet{"security": {IDs: []string{"security"}, Fresh: true, Resolved: true}}
	r := Requirement{Selector: Group, Group: "security", Count: 1}
	local := EvaluateRequirement(r, groups, in)
	if !local.Allowed || !local.ReviewsKnown || len(local.Requirements) != 1 || len(local.ReviewLogins) != 1 {
		t.Fatal("baseline owner obligation leaked into local pool", local)
	}
	baseline := Evaluate(Policy{Approvals: &Approvals{Default: r}, Groups: groups}, in)
	if baseline.Allowed || baseline.ReviewsKnown {
		t.Fatal("baseline owners waived", baseline)
	}
	for _, r := range []Requirement{{Selector: None}, {Selector: Group, Group: "security", Count: 0}} {
		if EvaluateRequirement(r, groups, in).Allowed {
			t.Fatal("invalid additive requirement allowed")
		}
	}
	in.Current.Generation++
	if got := EvaluateRequirement(r, groups, in); got.Allowed || got.ReviewsKnown {
		t.Fatal("stale classification allowed", got)
	}
}
