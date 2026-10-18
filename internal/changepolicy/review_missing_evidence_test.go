package changepolicy

import "testing"

func TestReviewMissingPolicyDigestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, classificationDigest, currentDigest string
		wantAllowed                               bool
	}{
		{"known-current-policy", "p", "p", true},
		{"stale-classification", "old", "p", false},
		{"missing-classification-digest", "", "p", false},
		{"missing-current-digest", "p", "", false},
		{"both-unresolved", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewInput()
			in.Classification.Inputs.PolicyDigest = tc.classificationDigest
			in.Current.PolicyDigest = tc.currentDigest
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: Human, Count: 2}, Rules: []Rule{
				{When: Match{Labels: []string{"docs"}}, Require: Requirement{Selector: None}},
			}}}
			got := Evaluate(p, in)
			if got.Allowed != tc.wantAllowed {
				t.Fatalf("unresolved policy accepted docs exemption with zero approvals: %+v", got)
			}
		})
	}
}

func TestReviewKnownNonwriterDoesNotMakeEligibilityUnknown(t *testing.T) {
	for _, kind := range []AccountKind{"", HumanAccount, AgentAccount} {
		t.Run(string(kind), func(t *testing.T) {
			in := reviewInput()
			reviewActor(&in, "alice", HumanAccount, 1)
			reviewActor(&in, "outsider", kind, 1)
			a := in.Actors["outsider"]
			a.Grant.Write = False
			in.Actors["outsider"] = a
			p := Policy{Approvals: &Approvals{Default: Requirement{Selector: Human, Count: 1}}}
			e := Evaluate(p, in)
			if !e.Allowed || !e.ReviewsKnown {
				t.Fatalf("proven nonwriter poisoned fresh human approval: %+v", e)
			}
		})
	}
}
