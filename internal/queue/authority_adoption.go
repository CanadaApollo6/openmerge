package queue

import (
	"reflect"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// BindAuthorityDraft constructs adoption only from a matching authenticated
// proposed event and a new complete read. Store.AdoptAuthorityPin authenticates
// the event and appends this pin atomically; activation wiring belongs to PR 9.
func BindAuthorityDraft(pin PolicyAuthorityApplied, id int64, draft WriteRestrictionVerified, started, at time.Time, fresh authority.Observation) PolicyAuthorityApplied {
	pin = *cloneAuthority(&pin)
	if pin.OpenMerge == nil {
		return pin
	}
	pin.OpenMerge.Activation = nil
	fingerprint, err := fresh.Fingerprint()
	if err != nil || draft.Validate() != nil || draft.Scope != "proposed" || id <= 0 || !reflect.DeepEqual(draft.Binding, pin.OpenMerge.Binding) || started.Before(draft.VerifiedAt) || at.Before(started) || fingerprint != draft.ProtectionFingerprint {
		return pin
	}
	pin.OpenMerge.Activation = &authority.Activation{
		Proposed:              authority.ProposedVerification{EventID: id, Scope: "proposed", Binding: draft.Binding, ProtectionFingerprint: draft.ProtectionFingerprint, SeeingReadAt: draft.VerifiedAt},
		ProtectionFingerprint: fingerprint, SeeingReadAt: at, Evidence: authority.CloneObservation(fresh),
	}
	return pin
}
