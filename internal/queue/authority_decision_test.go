package queue

import "testing"

func TestAuthorityDecisionWithoutApprovals(t *testing.T) {
	s, _, p, fresh, land, start, _ := landingDecisionSetup(t)
	p.Approvals = nil
	p.RequirePolicyOwners = true
	d := goldenLandingDecision(t, s, p, fresh, land, start)
	if len(d.Approval.Requirements) != 0 {
		t.Fatal("ordinary authority change acquired reviewer floor", d.Approval.Requirements)
	}
	if err := s.ValidateLandingDecisions(p, land, start, []ApprovalEligibilityObserved{fresh}, []LandingDecisionRecorded{d}); err != nil {
		t.Fatal(err)
	}
	fresh.Basis.Head = "changed"
	if err := s.ValidateLandingDecisions(p, land, start, []ApprovalEligibilityObserved{fresh}, []LandingDecisionRecorded{d}); err == nil {
		t.Fatal("head change accepted")
	}
}
