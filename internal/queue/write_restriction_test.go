package queue

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func restrictionState(t *testing.T) (State, WriteRestrictionVerified) {
	t.Helper()
	v := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	pin.OpenMerge.Activation = nil
	s := Apply(State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	return Apply(s, pin), v
}
func restrictionRead(s State, v WriteRestrictionVerified, at time.Time, o authority.Observation) State {
	return Apply(s, WriteRestrictionObserved{Binding: v.Binding, Basis: s.RulesSourceRevision(WriteRestriction), ObservedAt: at, Evidence: o})
}
func violatedEvidence(v WriteRestrictionVerified) authority.Observation {
	o := authority.CloneObservation(v.Evidence)
	a := authority.BypassActor{Kind: "user", ID: "rogue", Mode: "always"}
	o.Protections[0].BypassActors = append(o.Protections[0].BypassActors, a)
	o.Findings = []authority.Finding{{Element: "actor/rogue", Kind: "unexpected_bypass", ProtectionID: "repository_ruleset/base-10", Actor: &a}, {Element: "structure/deletion", Kind: "missing_rule", ProtectionID: "repository_ruleset/base-10"}}
	return o
}
func snapshotBytes(t *testing.T, s State) []byte {
	t.Helper()
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWriteRestrictionElementRepairStalenessAndIsolation(t *testing.T) {
	s, v := restrictionState(t)
	if got := s.WriteRestrictionStatus(); got.State != "unverified" || !got.LastObservedAt.IsZero() {
		t.Fatal(got)
	}
	s = Apply(s, RulesSourceBlocked{Source: Permissions, Problems: []string{"permissions"}})
	s = Apply(s, RulesSourceBlocked{Source: ClassicProtection, Problems: []string{"classic"}})
	s = Apply(s, RulesSourceBlocked{Source: Rulesets, Problems: []string{"rulesets"}})
	at := v.VerifiedAt
	before := snapshotBytes(t, s)
	blocked := restrictionRead(s, v, at, violatedEvidence(v))
	if !bytes.Equal(before, snapshotBytes(t, s)) {
		t.Fatal("Apply mutated prior state")
	}
	if len(blocked.WriteRestrictionStatus().Findings) != 2 {
		t.Fatal(blocked.WriteRestrictionStatus())
	}
	hidden := authority.CloneObservation(v.Evidence)
	hidden.Protections[0].BypassVisibility = authority.Hidden
	hidden.Protections[0].BypassActors = []authority.BypassActor{}
	repaired := restrictionRead(blocked, v, at.Add(time.Minute), hidden)
	if got := repaired.WriteRestrictionStatus(); len(got.Findings) != 1 || got.Findings[0].Element != "actor/rogue" || got.State != "violated" {
		t.Fatal(got)
	}
	// A delayed seeing pass started before the failure cannot clear it.
	stale := WriteRestrictionObserved{Binding: v.Binding, Basis: 0, ObservedAt: at.Add(2 * time.Minute), Evidence: v.Evidence}
	if got := Apply(repaired, stale); !bytes.Equal(snapshotBytes(t, repaired), snapshotBytes(t, got)) {
		t.Fatal("stale positive changed state")
	}
	// A new negative invalidates an earlier read, even from an old source basis.
	olderNegative := stale
	olderNegative.Evidence = violatedEvidence(v)
	olderNegative.ObservedAt = at.Add(-time.Hour)
	negative := Apply(repaired, olderNegative)
	if negative.RulesSourceRevision(WriteRestriction) <= repaired.RulesSourceRevision(WriteRestriction) || len(negative.WriteRestrictionStatus().Findings) != 2 {
		t.Fatal("negative lost")
	}
	cleared := restrictionRead(negative, v, at.Add(3*time.Minute), v.Evidence)
	if len(cleared.RulesSourceProblems(WriteRestriction)) != 0 || len(cleared.RulesProblems()) != 3 {
		t.Fatal(cleared.RulesSources())
	}
	for _, source := range []RulesSource{Rulesets, ClassicProtection, Permissions} {
		if cleared.RulesSourceRevision(source) != 1 {
			t.Fatal("source crossed", source)
		}
	}
	// Unknown/failed reads leave both ages, findings and source revisions intact.
	failed := authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}
	if got := restrictionRead(cleared, v, at.Add(4*time.Minute), failed); !bytes.Equal(snapshotBytes(t, cleared), snapshotBytes(t, got)) {
		t.Fatal("failure refreshed state")
	}
	for _, mutate := range []func(*authority.PolicyBinding){func(b *authority.PolicyBinding) { b.EffectiveDigest = strings.Repeat("c", 64) }, func(b *authority.PolicyBinding) { b.CoverageDigest = strings.Repeat("c", 64) }, func(b *authority.PolicyBinding) { b.ResolvedActor.Team.TeamID = "different" }} {
		event := WriteRestrictionObserved{Binding: *cloneAuthority(&v.Binding), Basis: negative.RulesSourceRevision(WriteRestriction), ObservedAt: at.Add(4 * time.Minute), Evidence: v.Evidence}
		mutate(&event.Binding)
		if got := Apply(negative, event); !bytes.Equal(snapshotBytes(t, negative), snapshotBytes(t, got)) {
			t.Fatal("wrong binding cleared")
		}
	}
}

func TestProposedViolationDoesNotChangeGitHubPlan(t *testing.T) {
	_, v := restrictionState(t)
	v.Evidence = violatedEvidence(v)
	v.ProtectionFingerprint, _ = v.Evidence.Fingerprint()
	s := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	for _, e := range []Event{PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true}, EnqueueRequested{PR: 1, At: v.VerifiedAt}} {
		s = Apply(s, e)
	}
	cfg := Config{MergeMethod: Merge}
	next := Apply(s, v)
	if !reflect.DeepEqual(Plan(cfg, s, v.VerifiedAt), Plan(cfg, next, v.VerifiedAt)) || !reflect.DeepEqual(s.Blocks(cfg), next.Blocks(cfg)) || next.AuthorityPin() != nil || len(next.RulesSources()) != 0 || next.WriteRestrictionStatus().State != "unverified" {
		t.Fatal("proposed evidence changed active github state")
	}
}

func TestAuthorityHypotheticalClearAndRestart(t *testing.T) {
	s, v := restrictionState(t)
	for _, e := range []Event{PRObserved{PR: 1, HeadSHA: "head", Trusted: true, Eligible: true}, EnqueueRequested{PR: 1, At: v.VerifiedAt}} {
		s = Apply(s, e)
	}
	cfg := Config{MergeMethod: Merge, ConfigDigest: s.ConfigDigest}
	actions := Plan(cfg, s, v.VerifiedAt)
	if len(actions) == 0 {
		t.Fatal("no baseline action")
	}
	s = restrictionRead(s, v, v.VerifiedAt, violatedEvidence(v))
	before := snapshotBytes(t, s)
	if !DeferredByPause(cfg, s, actions[0], v.VerifiedAt) {
		t.Fatal("dedicated block was not hypothetically cleared")
	}
	if !bytes.Equal(before, snapshotBytes(t, s)) {
		t.Fatal("hypothetical clear mutated durable projection")
	}
	restored, err := DecodeSnapshot(before)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.WriteRestrictionStatus(), restored.WriteRestrictionStatus()) || !reflect.DeepEqual(Plan(cfg, s, v.VerifiedAt), Plan(cfg, restored, v.VerifiedAt)) {
		t.Fatal("restart lost findings or next actions")
	}
	for _, e := range []Event{RulesSourceCleared{Source: WriteRestriction}, RulesSourceBlocked{Source: WriteRestriction, Problems: []string{"invalid"}}} {
		if _, _, err := EncodeEvent(e); err == nil {
			t.Fatal("legacy source event accepted authority")
		}
	}
	cleared := restrictionRead(restored, v, v.VerifiedAt.Add(time.Minute), v.Evidence)
	if len(cleared.RulesSourceProblems(WriteRestriction)) != 0 {
		t.Fatal("restored source did not clear")
	}
}

func TestAuthorityDraftBinding(t *testing.T) {
	s, v := restrictionState(t)
	pin := *s.AuthorityPin()
	in := adoptionInput(t, &pin, &v)
	for _, name := range []string{"match", "digest", "actor", "selector", "method", "inventory", "hidden", "incomplete", "old-read"} {
		t.Run(name, func(t *testing.T) {
			candidate := *cloneAuthority(&pin)
			fresh := authority.CloneObservation(v.Evidence)
			started := v.VerifiedAt.Add(time.Second)
			switch name {
			case "digest":
				candidate.OpenMerge.Binding.EffectiveDigest = strings.Repeat("c", 64)
			case "actor":
				candidate.OpenMerge.Binding.ResolvedActor.Team.TeamID = "new"
			case "selector":
				candidate.OpenMerge.Binding.CoverageDigest = strings.Repeat("c", 64)
			case "method":
				candidate.OpenMerge.Binding.MergeMethod = "merge"
			case "inventory":
				fresh.Protections[0].ID = "replaced"
				fresh.Coverage[0].ProtectionIDs = []string{"repository_ruleset/replaced"}
			case "hidden":
				fresh.Protections[0].BypassVisibility = authority.Hidden
				fresh.Protections[0].BypassActors = []authority.BypassActor{}
			case "incomplete":
				fresh.Inventory = authority.ReadFailure
			case "old-read":
				started = v.VerifiedAt.Add(-time.Second)
			}
			result := BindAuthorityDraft(candidate, 123, v, started, started.Add(time.Second), in, fresh)
			if (result.OpenMerge.Activation != nil) != (name == "match") {
				t.Fatal(result)
			}
			if name == "match" && result.Validate() != nil {
				t.Fatal(result.Validate())
			}
		})
	}
}

func TestAuthoritySameBindingPinPreservesViolation(t *testing.T) {
	s, v := restrictionState(t)
	s = restrictionRead(s, v, v.VerifiedAt, violatedEvidence(v))
	pin := *s.AuthorityPin()
	pin.GoverningCommit = "formatting-only"
	s = Apply(s, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	revision := s.RulesSourceRevision(WriteRestriction)
	s = Apply(s, pin)
	if s.WriteRestrictionStatus().State != "violated" || s.RulesSourceRevision(WriteRestriction) != revision {
		t.Fatal("same-policy pin erased violation")
	}
	pin.EffectiveDigest = strings.Repeat("c", 64)
	pin.OpenMerge.Binding.EffectiveDigest = pin.EffectiveDigest
	s = Apply(s, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	s = Apply(s, pin)
	if s.WriteRestrictionStatus().State != "unverified" || len(s.RulesSourceProblems(WriteRestriction)) != 0 {
		t.Fatal("new policy imported old evidence")
	}
	old := WriteRestrictionObserved{Binding: v.Binding, Basis: 0, ObservedAt: v.VerifiedAt.Add(time.Minute), Evidence: v.Evidence}
	if got := Apply(s, old); !bytes.Equal(snapshotBytes(t, s), snapshotBytes(t, got)) {
		t.Fatal("old digest modified new policy")
	}
}
