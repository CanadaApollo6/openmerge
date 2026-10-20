package status

import (
	"reflect"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Eligibility events have no policy digest of their own. Pin the policy and
// classification at their position in events.id order, never by Basis.At.
// These display-only pins do not alter queue snapshots or landing authority.
type briefEligibilityPin struct {
	basis        queue.ApprovalBasis
	inputs       cp.ClassificationInputs
	configDigest string
	stale        bool
}

func pinBriefEligibility(pins map[queue.ChangeID]briefEligibilityPin, event queue.Event, before, s queue.State) {
	var e queue.ApprovalEligibilityObserved
	switch v := event.(type) {
	case queue.ConfigApplied:
		if before.ConfigDigest != s.ConfigDigest {
			for id, pin := range pins {
				pin.stale = true
				pins[id] = pin
			}
		}
		return
	case queue.ApprovalEligibilityObserved:
		e = v
	case queue.ApprovalInputsChanged:
		e = v.Eligibility
	default:
		return
	}
	facts, _ := s.ApprovalFacts(e.Basis.ChangeID)
	prior, _ := before.ApprovalFacts(e.Basis.ChangeID)
	if !reflect.DeepEqual(facts.Eligibility, &e) || reflect.DeepEqual(prior.Eligibility, facts.Eligibility) {
		return // Rejected or duplicate events cannot renew an old policy pin.
	}
	stored, _ := s.Classification(e.Basis.ChangeID, e.Basis.Revision)
	pins[e.Basis.ChangeID] = briefEligibilityPin{basis: e.Basis, inputs: stored.Evidence.Classification.Inputs, configDigest: s.ConfigDigest}
}

func recordedBriefProgress(f brief.Fact, policy cp.Policy, s queue.State, facts queue.ApprovalFacts, configDigest string, pin briefEligibilityPin) brief.Fact {
	code, explanation := "", ""
	switch {
	case facts.Eligibility == nil || pin.basis != facts.Eligibility.Basis:
		code, explanation = "eligibility_pending", "eligibility pending gate read"
	case pin.stale || !reflect.DeepEqual(pin.inputs, f.Classification.Inputs) || pin.configDigest != configDigest || pin.configDigest != s.ConfigDigest:
		code, explanation = "eligibility_stale", "recorded eligibility predates the current revision, head or policy; pending gate read"
	}
	if code == "" {
		identity, err := brief.Identity(f)
		if err == nil {
			// Resolve progress using the last committed observation, while retaining
			// the original classification and requirements for receipt identity.
			return brief.OverlayProgress(f, s.EvaluateApprovals(policy, identity, *facts.Eligibility))
		}
		code, explanation = "eligibility_pending", "eligibility pending gate read"
	}
	for i := range f.Obligations {
		r := &f.Obligations[i]
		// Structural failures remain useful even before eligibility is read.
		if r.Code != "approval_required" && r.Code != "unknown_eligibility" && r.Code != "met" {
			continue
		}
		r.Met, r.Unresolved, r.Code, r.Explanation = false, true, code, explanation
		r.EligibleIDs, r.ApprovedIDs = []string{}, []string{}
	}
	return f
}
