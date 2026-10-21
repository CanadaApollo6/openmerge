package queue

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// AuthorityVerificationInput retains the interpretation expectations used by a
// draft adoption. Historical pins omit it and remain readable, but their raw
// activation evidence cannot establish verified authority.
type AuthorityVerificationInput struct {
	BaseSelectors []authority.BaseSelector `json:"base_selectors"`
	ExpectedRefs  []string                 `json:"expected_refs"`
	AppID         string                   `json:"app_id"`
}

func authorityInput(binding authority.PolicyBinding, input AuthorityVerificationInput) forge.WriteRestrictionInput {
	return forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: binding.Repository}, Binding: binding, BaseSelectors: input.BaseSelectors, ExpectedRefs: input.ExpectedRefs, AppID: input.AppID}
}

func validAuthorityInput(binding authority.PolicyBinding, in forge.WriteRestrictionInput) bool {
	if !reflect.DeepEqual(binding, in.Binding) || forge.ValidateWriteRestrictionInput(in) != nil {
		return false
	}
	prefix := strconv.FormatInt(binding.InstallationID, 10) + "/" + binding.Repository + "/"
	base, ok := strings.CutPrefix(binding.QueueKey, prefix)
	return ok && base != "" && slices.Contains(in.ExpectedRefs, "refs/heads/"+base)
}

func interpretedAuthorityEvidence(in forge.WriteRestrictionInput, evidence authority.Observation) (authority.Observation, error) {
	// The interpreter accumulates findings, so discard caller interpretation
	// before deriving the complete answer from typed facts and actual inputs.
	evidence = authority.CloneObservation(evidence)
	evidence.Findings = []authority.Finding{}
	return forge.InterpretWriteRestriction(in, evidence)
}

func sameAuthorityFindings(a, b []authority.Finding) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	less := func(a, b authority.Finding) int { return strings.Compare(a.Element, b.Element) }
	slices.SortFunc(a, less)
	slices.SortFunc(b, less)
	return reflect.DeepEqual(a, b)
}

// AuthorityDraftAdopted verifies the durable interpretation context as well as
// the activation's raw fingerprint. Replaying a historical or crafted pin with
// raw evidence alone must never turn the active projection into verified.
func AuthorityDraftAdopted(pin PolicyAuthorityApplied) bool {
	if pin.OpenMerge == nil || pin.OpenMerge.Activation == nil || pin.OpenMerge.VerificationInput == nil || pin.Validate() != nil {
		return false
	}
	in := authorityInput(pin.OpenMerge.Binding, *pin.OpenMerge.VerificationInput)
	if !validAuthorityInput(pin.OpenMerge.Binding, in) {
		return false
	}
	a := pin.OpenMerge.Activation
	interpreted, err := interpretedAuthorityEvidence(in, a.Evidence)
	if err != nil || !sameAuthorityFindings(interpreted.Findings, a.Evidence.Findings) {
		return false
	}
	fingerprint, err := interpreted.Fingerprint()
	return err == nil && fingerprint == a.ProtectionFingerprint
}

// BindAuthorityDraft constructs adoption only from a matching authenticated
// proposed event and a new complete read interpreted against actual forge
// expectations. Store.AdoptAuthorityPin authenticates the source event and
// appends this pin atomically; this helper performs no runtime activation.
func BindAuthorityDraft(pin PolicyAuthorityApplied, id int64, draft WriteRestrictionVerified, started, at time.Time, in forge.WriteRestrictionInput, fresh authority.Observation) PolicyAuthorityApplied {
	pin = *cloneAuthority(&pin)
	if pin.OpenMerge == nil {
		return pin
	}
	pin.OpenMerge.Activation = nil
	pin.OpenMerge.VerificationInput = nil
	if !validAuthorityInput(pin.OpenMerge.Binding, in) {
		return pin
	}
	fresh, err := interpretedAuthorityEvidence(in, fresh)
	if err != nil {
		return pin
	}
	fingerprint, err := fresh.Fingerprint()
	if err != nil || draft.Validate() != nil || draft.Scope != "proposed" || id <= 0 || !reflect.DeepEqual(draft.Binding, pin.OpenMerge.Binding) || started.Before(draft.VerifiedAt) || at.Before(started) || fingerprint != draft.ProtectionFingerprint || !sameAuthorityFindings(fresh.Findings, draft.Evidence.Findings) {
		return pin
	}
	pin.OpenMerge.VerificationInput = cloneAuthority(&AuthorityVerificationInput{BaseSelectors: in.BaseSelectors, ExpectedRefs: in.ExpectedRefs, AppID: in.AppID})
	pin.OpenMerge.Activation = &authority.Activation{
		Proposed:              authority.ProposedVerification{EventID: id, Scope: "proposed", Binding: draft.Binding, ProtectionFingerprint: draft.ProtectionFingerprint, SeeingReadAt: draft.VerifiedAt},
		ProtectionFingerprint: fingerprint, SeeingReadAt: at, Evidence: fresh,
	}
	return pin
}
