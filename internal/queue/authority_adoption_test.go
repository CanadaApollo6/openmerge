package queue

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func adoptionInput(t *testing.T, pin *PolicyAuthorityApplied, draft *WriteRestrictionVerified) forge.WriteRestrictionInput {
	t.Helper()
	selectors := []authority.BaseSelector{{DefaultBranch: true}, {Pattern: "release/**"}}
	digest, err := authority.CoverageDigest("main", selectors)
	if err != nil {
		t.Fatal(err)
	}
	draft.Binding.CoverageDigest = digest
	pin.CoverageDigest = digest
	pin.OpenMerge.Binding = draft.Binding
	in := forge.WriteRestrictionInput{Repo: forge.RepoRef{Path: draft.Binding.Repository}, Binding: draft.Binding, BaseSelectors: selectors, ExpectedRefs: []string{"refs/heads/main"}, AppID: "5383059"}
	draft.Evidence, err = interpretedAuthorityEvidence(in, draft.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	draft.ProtectionFingerprint, err = draft.Evidence.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestAuthorityDraftAdoptionInterpretsFactsAndRequiresMatchingFindings(t *testing.T) {
	s, draft := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &draft)
	rogue := authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}
	for _, name := range []string{"passing", "changed-findings", "stripped-draft-findings", "stripped-fresh-findings", "wrong-app", "wrong-base"} {
		t.Run(name, func(t *testing.T) {
			v := *cloneAuthority(&draft)
			input := *cloneAuthority(&in)
			fresh := authority.CloneObservation(v.Evidence)
			switch name {
			case "changed-findings":
				v.Evidence.Findings = []authority.Finding{{Element: "invented", Kind: "missing_rule", ProtectionID: "repository_ruleset/base-10"}}
			case "stripped-draft-findings", "stripped-fresh-findings":
				fresh.Protections[0].BypassActors = append(fresh.Protections[0].BypassActors, rogue)
				v.Evidence, _ = interpretedAuthorityEvidence(input, fresh)
				v.ProtectionFingerprint, _ = v.Evidence.Fingerprint()
				if name == "stripped-draft-findings" {
					v.Evidence.Findings = []authority.Finding{}
				}
			case "wrong-app":
				input.AppID = "different"
			case "wrong-base":
				input.ExpectedRefs = []string{"refs/heads/other"}
			}
			started := v.VerifiedAt.Add(time.Second)
			result := BindAuthorityDraft(pin, 123, v, started, started.Add(time.Second), input, fresh)
			want := name == "passing" || name == "stripped-fresh-findings"
			if AuthorityDraftAdopted(result) != want {
				t.Fatalf("adopted=%v, want %v", AuthorityDraftAdopted(result), want)
			}
			state := Apply(Apply(State{}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}), result)
			wantStatus := "unverified"
			if name == "passing" {
				wantStatus = "verified"
			}
			if name == "stripped-fresh-findings" {
				wantStatus = "violated"
			}
			if got := state.WriteRestrictionStatus().State; got != wantStatus {
				t.Fatalf("state=%s want=%s", got, wantStatus)
			}
			restored, err := DecodeSnapshot(snapshotBytes(t, state))
			if err != nil || restored.WriteRestrictionStatus().State != wantStatus {
				t.Fatal("snapshot lost interpreted adoption", err)
			}
		})
	}
}

func TestAuthorityRawActivationCannotFoldVerified(t *testing.T) {
	s, draft := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &draft)
	started := draft.VerifiedAt.Add(time.Second)
	valid := BindAuthorityDraft(pin, 123, draft, started, started.Add(time.Second), in, draft.Evidence)
	for _, name := range []string{"historical", "missing-context", "stripped-findings", "raw-coverage", "wrong-context"} {
		t.Run(name, func(t *testing.T) {
			crafted := *cloneAuthority(&valid)
			switch name {
			case "historical":
				crafted = authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
			case "missing-context":
				crafted.OpenMerge.VerificationInput = nil
			case "stripped-findings":
				a := authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}
				crafted.OpenMerge.Activation.Evidence.Protections[0].BypassActors = append(crafted.OpenMerge.Activation.Evidence.Protections[0].BypassActors, a)
			case "raw-coverage":
				crafted.OpenMerge.Activation.Evidence.Coverage = crafted.OpenMerge.Activation.Evidence.Coverage[:1]
			case "wrong-context":
				crafted.OpenMerge.VerificationInput.AppID = "wrong"
			}
			// Keep both fingerprints internally valid: the missing interpretation
			// must prevent verification even when raw wire validation succeeds.
			a := crafted.OpenMerge.Activation
			a.ProtectionFingerprint, _ = a.Evidence.Fingerprint()
			a.Proposed.ProtectionFingerprint = a.ProtectionFingerprint
			_, raw, err := EncodeEvent(crafted)
			if err != nil {
				t.Fatal(err)
			}
			event, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			state := Apply(Apply(State{}, ConfigApplied{Commit: crafted.GoverningCommit, Digest: crafted.EffectiveDigest}), event)
			if got := state.WriteRestrictionStatus(); got.State != "unverified" || !got.LastVerifiedAt.IsZero() {
				t.Fatal(got)
			}
			encoded := snapshotBytes(t, state)
			restored, err := DecodeSnapshot(encoded)
			if err != nil || restored.WriteRestrictionStatus().State != "unverified" {
				t.Fatal("restart trusted raw activation", err)
			}
		})
	}
	if strings.Contains(string(snapshotBytes(t, s)), "verification_input") {
		t.Fatal("draft binding mutated prior state")
	}
}

func TestAuthoritySnapshotRejectsRawActivationVerification(t *testing.T) {
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	state := Apply(Apply(State{}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}), pin)
	var snapshot map[string]any
	if err := json.Unmarshal(snapshotBytes(t, state), &snapshot); err != nil {
		t.Fatal(err)
	}
	// Reproduce the baseline materialized projection, independently of the
	// hardened fold: raw activation used to fill these values during replay.
	projection := snapshot["Authority"].(map[string]any)
	projection["complete"] = true
	projection["last_observed_at"] = pin.OpenMerge.Activation.SeeingReadAt
	projection["last_verified_at"] = pin.OpenMerge.Activation.SeeingReadAt
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw); err == nil || !strings.Contains(err.Error(), "replay journal and reverify authority") {
		t.Fatal("raw activation snapshot retained verified state", err)
	}
}

func TestAuthorityVerificationInputRequiresActivationAndBoundedSize(t *testing.T) {
	s, draft := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &draft)
	started := draft.VerifiedAt.Add(time.Second)
	valid := BindAuthorityDraft(pin, 123, draft, started, started.Add(time.Second), in, draft.Evidence)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"without-activation", "too-many-selectors", "too-many-refs", "oversized-app", "oversized-selector", "oversized-ref"} {
		t.Run(name, func(t *testing.T) {
			bad := *cloneAuthority(&valid)
			input := bad.OpenMerge.VerificationInput
			switch name {
			case "without-activation":
				bad.OpenMerge.Activation = nil
			case "too-many-selectors":
				input.BaseSelectors = make([]authority.BaseSelector, 1001)
			case "too-many-refs":
				input.ExpectedRefs = make([]string, 1001)
			case "oversized-app":
				input.AppID = strings.Repeat("x", 1<<20)
			case "oversized-selector":
				input.BaseSelectors[0].Pattern = strings.Repeat("x", 1<<20)
			case "oversized-ref":
				input.ExpectedRefs[0] = strings.Repeat("x", 1<<20)
			}
			if _, _, err := EncodeEvent(bad); err == nil {
				t.Fatal("encoded invalid verification input")
			}
			raw, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeAuthorityEvent[PolicyAuthorityApplied](raw); err == nil {
				t.Fatal("decoded invalid verification input")
			}
		})
	}
}

func TestAuthorityDraftAdoptionRejectsRawCoverageWithSameFingerprint(t *testing.T) {
	s, draft := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &draft)
	started := draft.VerifiedAt.Add(time.Second)
	crafted := BindAuthorityDraft(pin, 123, draft, started, started.Add(time.Second), in, draft.Evidence)
	evidence := &crafted.OpenMerge.Activation.Evidence
	if len(evidence.Coverage) < 2 {
		t.Fatal("need multiple coverage records")
	}
	// Fingerprints canonicalize coverage ordering. A crafted raw representation
	// can therefore retain the fingerprint while differing from interpretation.
	slices.Reverse(evidence.Coverage)
	fingerprint, err := evidence.Fingerprint()
	if err != nil || fingerprint != crafted.OpenMerge.Activation.ProtectionFingerprint {
		t.Fatal("probe did not preserve fingerprint", err)
	}
	_, raw, err := EncodeEvent(crafted)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if AuthorityDraftAdopted(event.(PolicyAuthorityApplied)) {
		t.Fatal("adopted raw evidence differing from interpreted evidence")
	}
	state := Apply(Apply(State{}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}), event)
	if got := state.WriteRestrictionStatus(); got.State != "unverified" || !got.LastVerifiedAt.IsZero() {
		t.Fatal(got)
	}
}

func TestAuthorityAdoptedProvenanceSurvivesSameBindingRebind(t *testing.T) {
	s, draft := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &draft)
	started := draft.VerifiedAt.Add(time.Second)
	adopted := BindAuthorityDraft(pin, 123, draft, started, started.Add(time.Second), in, draft.Evidence)
	s = Apply(Apply(State{}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}), adopted)
	if s.WriteRestrictionStatus().State != "verified" {
		t.Fatal(s.WriteRestrictionStatus())
	}
	// A same-binding pin updates only the governing revision; the original
	// interpreted source remains the proof for the preserved verification.
	pin.GoverningCommit = "formatting-only"
	s = Apply(s, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	s = Apply(s, pin)
	restored, err := DecodeSnapshot(snapshotBytes(t, s))
	if err != nil || restored.WriteRestrictionStatus().State != "verified" || restored.AuthorityPin().GoverningCommit != pin.GoverningCommit {
		t.Fatal("same-policy rebind lost adopted source", err)
	}
	if restored.authority.Adopted == nil || restored.authority.Adopted.GoverningCommit != adopted.GoverningCommit {
		t.Fatal("lost original durable adoption provenance")
	}
}
