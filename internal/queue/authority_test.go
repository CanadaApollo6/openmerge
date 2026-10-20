package queue

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func authorityFixture[T Event](t *testing.T, name string) T {
	t.Helper()
	path := "testdata/wire/v1/event_" + name + ".json"
	if name == "policy_authority_applied" {
		path = "testdata/wire/v1/variants/policy_authority_applied_activation.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return e.(T)
}

func TestAuthorityObservationVariants(t *testing.T) {
	paths, err := filepath.Glob("testdata/wire/v1/variants/write_restriction_*.json")
	if err != nil || len(paths) != 4 {
		t.Fatal("missing evidence variants", paths, err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			event, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			_, encoded, err := EncodeEvent(event)
			if err != nil || !bytes.Equal(raw, encoded) {
				t.Fatal("evidence variant changed", err)
			}
			observed, ok := event.(WriteRestrictionObserved)
			if !ok {
				return
			}
			declared, err := observed.Binding.ResolvedActor.BypassIdentity()
			if err != nil {
				t.Fatal(err)
			}
			kinds := map[string]bool{}
			for _, finding := range observed.Evidence.Findings {
				if finding.Kind != "unexpected_bypass" || finding.Actor == nil || *finding.Actor == declared {
					t.Fatal("unauthorized actor lost its finding", finding)
				}
				kinds[finding.Actor.Kind] = true
			}
			for _, kind := range []string{"app", "team", "repository_role", "user", "organization_admin", "deploy_key", "enterprise_owner", "enterprise_role", "unknown"} {
				if !kinds[kind] {
					t.Fatal("missing unexpected_bypass finding for", kind)
				}
			}
			// Findings require the actor to be present in the visible inventory.
			observed.Evidence.Protections[0].BypassActors = []authority.BypassActor{}
			if observed.Validate() == nil {
				t.Fatal("finding accepted without observed actor")
			}
		})
	}
}

func TestCloneStateOwnsAuthorityEvidence(t *testing.T) {
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	observed := authorityFixture[WriteRestrictionObserved](t, "write_restriction_observed")
	verified := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	applied := verified
	applied.Scope = "applied"
	state := State{}
	for _, event := range []Event{ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, pin, observed, applied, verified} {
		if v, ok := event.(WriteRestrictionVerified); ok && v.Scope == "applied" {
			v.Basis = state.RulesSourceRevision(WriteRestriction)
			event = v
		}
		state = Apply(state, event)
	}
	before, err := EncodeSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	cloned := cloneState(state)
	cloned.authority.Pin.OpenMerge.Binding.ResolvedActor.Team.TeamID = "changed"
	cloned.authority.Pin.OpenMerge.Activation.Evidence.Protections[0].Includes[0] = "changed"
	cloned.authority.Observed.Evidence.Protections[0].Rules[1].Parameters[0].Values[0] = "changed"
	cloned.authority.Observed.Evidence.Coverage[0].ProtectionIDs[0] = "changed"
	cloned.authority.Observed.Evidence.Findings[0].Element = "changed"
	cloned.authority.Verified.Evidence.Protections[0].BypassActors[0].ID = "changed"
	cloned.authority.Proposed.Evidence.Protections[0].Includes[0] = "changed"
	cloned.authority.Pin = nil
	after, err := EncodeSnapshot(state)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("clone aliases authority evidence", err)
	}
	if cloneState(State{}).authority != nil {
		t.Fatal("clone populated legacy authority")
	}
}

func TestAuthoritySnapshotTailAndIsolation(t *testing.T) {
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	observed := authorityFixture[WriteRestrictionObserved](t, "write_restriction_observed")
	verified := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	applied := verified
	applied.Scope = "applied"
	rollback := pin
	rollback.Authority = authority.GitHub
	rollback.OpenMerge = nil
	events := []Event{ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest}, verified, pin, observed, applied, rollback}
	initial := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	full := initial
	for i, e := range events {
		before, _ := EncodeSnapshot(full)
		next := Apply(full, e)
		after, _ := EncodeSnapshot(full)
		if !bytes.Equal(before, after) {
			t.Fatal("Apply mutated input", i)
		}
		full = next
	}
	want, err := EncodeSnapshot(full)
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut <= len(events); cut++ {
		s := initial
		for _, e := range events[:cut] {
			s = Apply(s, e)
		}
		raw, err := EncodeSnapshot(s)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DecodeSnapshot(raw)
		if err != nil {
			t.Fatal(cut, err)
		}
		for _, e := range events[cut:] {
			restored = Apply(restored, e)
		}
		got, err := EncodeSnapshot(restored)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("snapshot-tail mismatch at %d", cut)
		}
		if !reflect.DeepEqual(Plan(Config{}, full, time.Time{}), Plan(Config{}, restored, time.Time{})) {
			t.Fatal("snapshot-tail plan mismatch")
		}
	}
	if initial.authority != nil {
		t.Fatal("legacy state acquired authority")
	}
	s := Apply(Apply(initial, events[0]), pin)
	pinned := s.AuthorityPin()
	pinned.OpenMerge.Binding.ResolvedActor.Team.TeamID = "changed"
	if s.AuthorityPin().OpenMerge.Binding.ResolvedActor.Team.TeamID == "changed" {
		t.Fatal("getter aliases projection")
	}
	pin.OpenMerge.Activation.Evidence.Protections[0].Includes[0] = "changed"
	if s.AuthorityPin().OpenMerge.Activation.Evidence.Protections[0].Includes[0] == "changed" {
		t.Fatal("event aliases projection")
	}
	stale := observed
	stale.Evidence = authority.CloneObservation(verified.Evidence)
	stale.Basis = 2
	stale.ObservedAt = observed.ObservedAt.Add(-time.Second)
	s = Apply(s, observed)
	before, _ := EncodeSnapshot(s)
	s = Apply(s, stale)
	after, _ := EncodeSnapshot(s)
	if !bytes.Equal(before, after) {
		t.Fatal("stale observation replaced evidence")
	}
	mismatch := observed
	mismatch.Binding.EffectiveDigest = strings.Repeat("c", 64)
	s = Apply(s, mismatch)
	after, _ = EncodeSnapshot(s)
	if !bytes.Equal(before, after) {
		t.Fatal("wrong-policy evidence accepted")
	}
	legacyBefore, _ := EncodeSnapshot(initial)
	proposed := Apply(initial, verified)
	if !reflect.DeepEqual(Plan(Config{}, initial, time.Time{}), Plan(Config{}, proposed, time.Time{})) {
		t.Fatal("proposed verification changed admission")
	}
	if proposed.authority.Proposed == nil || proposed.authority.Pin != nil {
		t.Fatal("proposed verification activated policy")
	}
	if !bytes.Contains(legacyBefore, []byte(`"Version":6`)) {
		t.Fatal("legacy version changed")
	}
}

func TestAuthoritySnapshotVersionGate(t *testing.T) {
	legacy := State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}
	raw, err := EncodeSnapshot(legacy)
	if err != nil {
		t.Fatal(err)
	}
	old, err := EncodeSnapshotV6(legacy)
	if err != nil || !bytes.Equal(raw, old) {
		t.Fatal("legacy encoder differs", err)
	}
	if bytes.Contains(raw, []byte(`"Authority"`)) || !bytes.Contains(raw, []byte(`"Version":6`)) {
		t.Fatal("legacy shape changed")
	}
	verified := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	state := Apply(legacy, verified)
	v7, err := EncodeSnapshot(state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(v7, []byte(`"Version":7`)) {
		t.Fatal("authority not v7")
	}
	if _, err := EncodeSnapshotV6(state); err == nil {
		t.Fatal("v6 erased authority")
	}
	for _, bad := range [][]byte{bytes.Replace(v7, []byte(`"Version":7`), []byte(`"Version":6`), 1), bytes.Replace(raw, []byte(`"Version":6`), []byte(`"Version":7`), 1), bytes.Replace(v7, []byte(`"proposed":`), []byte(`"Proposed":`), 1)} {
		if _, err := DecodeSnapshot(bad); err == nil {
			t.Fatal("accepted mismatched snapshot gate")
		}
	}

}

func TestAuthorityWireStrictAndVisibility(t *testing.T) {
	for _, name := range []string{"policy_authority_applied", "write_restriction_observed", "write_restriction_verified"} {
		raw, err := os.ReadFile("testdata/wire/v1/event_" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range [][]byte{
			bytes.Replace(raw, []byte(`"binding":`), []byte(`"Binding":`), 1),
			bytes.Replace(raw, []byte(`"default_branch":"main"`), []byte(`"default_branch":"main","extra":true`), 1),
			bytes.Replace(raw, []byte(`"default_branch":"main"`), []byte(`"default_branch":"main","default_branch":"main"`), 1),
			bytes.Replace(raw, []byte(`"default_branch":"main"`), []byte(`"default_branch":null`), 1),
			bytes.Replace(raw, []byte(`"default_branch":"main",`), nil, 1),
			bytes.Replace(raw, []byte(`"team":"Smart-Data-Ohio/break-glass"`), []byte(`"team":"Smart-Data-Ohio/break-glass","repository_admin":false`), 1),
		} {
			if bytes.Equal(bad, raw) {
				t.Fatalf("mutation missing in %s", name)
			}
			if _, err := DecodeEvent(bad); err == nil {
				t.Fatalf("accepted malformed %s: %s", name, bad)
			}
		}
	}
	observed := authorityFixture[WriteRestrictionObserved](t, "write_restriction_observed")
	if observed.Evidence.Inventory != authority.ReadFailure || observed.Evidence.Protections[0].BypassVisibility != authority.Hidden || observed.Evidence.Coverage[2].Visibility != authority.Absent || observed.Evidence.Coverage[3].Visibility != authority.ReadFailure {
		t.Fatal("visibility lost during decode")
	}
	if observed.Evidence.SeeingComplete() {
		t.Fatal("partial evidence claims full visibility")
	}
	verified := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	verified.Evidence.Protections[0].BypassVisibility = authority.Hidden
	verified.Evidence.Protections[0].BypassActors = []authority.BypassActor{}
	if _, _, err := EncodeEvent(verified); err == nil {
		t.Fatal("hidden read claimed verification")
	}
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	pin.Authority = authority.GitHub
	pin.OpenMerge = nil
	_, raw, err := EncodeEvent(pin)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || decoded.(PolicyAuthorityApplied).OpenMerge != nil {
		t.Fatal("github variant required actor", err)
	}
}

func TestAuthorityRepositoryAdminWire(t *testing.T) {
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	pin.OpenMerge.Activation = nil
	pin.OpenMerge.Binding.DeclaredActor = authority.Actor{RepositoryAdmin: true}
	pin.OpenMerge.Binding.ResolvedActor = authority.ResolvedActor{RepositoryAdmin: &authority.ResolvedRepositoryAdmin{RoleID: "adapter-resolved-admin-role"}}
	_, raw, err := EncodeEvent(pin)
	if err != nil {
		t.Fatal(err)
	}
	e, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e, pin) {
		t.Fatal("admin actor did not round trip")
	}
	pin.OpenMerge.Binding.ResolvedActor = authority.ResolvedActor{Team: &authority.ResolvedTeam{OrganizationID: "100", TeamID: "200"}}
	if _, _, err := EncodeEvent(pin); err == nil {
		t.Fatal("mismatched actor discriminants accepted")
	}
}

func TestAuthorityActivationFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire/v1/variants/policy_authority_applied_activation.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, encoded, err := EncodeEvent(event)
	if err != nil || !bytes.Equal(raw, encoded) {
		t.Fatal("activation fixture changed", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range missingOrNullFields(value) {
		if mutation.path == "data/openmerge/activation/missing" {
			continue
		}
		bad, _ := json.Marshal(mutation.value)
		if _, err := DecodeEvent(bad); err == nil {
			t.Fatal("accepted incomplete activation", mutation.path)
		}
	}
}

func TestAuthorityPinRebindsGoverningCommitWithSameDigest(t *testing.T) {
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	state := Apply(State{}, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	state = Apply(state, pin)
	oldCommit := state.ConfigCommit
	pin.GoverningCommit = "later-governing-commit"
	state = Apply(state, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	state = Apply(state, pin)
	if state.ConfigCommit != oldCommit || state.AuthorityPin().GoverningCommit != pin.GoverningCommit {
		t.Fatal("authority pin did not bind actual governing commit independently of legacy config fold")
	}
}

func TestAuthorityActivationRejectsMismatchedDraft(t *testing.T) {
	original := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	for _, change := range []func(*PolicyAuthorityApplied){
		func(p *PolicyAuthorityApplied) { p.OpenMerge.Activation.Proposed.Scope = "applied" },
		func(p *PolicyAuthorityApplied) { p.OpenMerge.Activation.Proposed.Binding.MergeMethod = "rebase" },
		func(p *PolicyAuthorityApplied) {
			p.OpenMerge.Activation.Proposed.ProtectionFingerprint = strings.Repeat("f", 64)
		},
		func(p *PolicyAuthorityApplied) {
			p.OpenMerge.Activation.SeeingReadAt = p.OpenMerge.Activation.Proposed.SeeingReadAt.Add(-time.Second)
		},
	} {
		p := cloneAuthority(&original)
		change(p)
		if _, _, err := EncodeEvent(*p); err == nil {
			t.Fatal("adopted mismatched proposed verification")
		}
	}
	proposed := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	s := Apply(State{}, proposed)
	s = Apply(s, ConfigApplied{Commit: original.GoverningCommit, Digest: original.EffectiveDigest})
	s = Apply(s, original)
	if s.authority.Proposed != nil {
		t.Fatal("pin rebind retained a candidate draft")
	}
}

func TestAuthorityLegacySiblingIsolation(t *testing.T) {
	approval, approvalConfig, _, _, _, start, _ := landingDecisionSetup(t)
	pin := authorityFixture[PolicyAuthorityApplied](t, "policy_authority_applied")
	observed := authorityFixture[WriteRestrictionObserved](t, "write_restriction_observed")
	proposed := authorityFixture[WriteRestrictionVerified](t, "write_restriction_verified")
	for _, depth := range []int{1, 4} {
		cfg := approvalConfig
		cfg.SpeculationMaxDepth = depth
		legacy := Apply(State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}, PRObserved{PR: 2, HeadSHA: "head", Eligible: true, Trusted: true})
		legacy = Apply(legacy, EnqueueRequested{PR: 2, At: start.At})
		siblings := []State{legacy, approval}
		before := make([][]byte, len(siblings))
		plans := make([][]Action, len(siblings))
		for i, s := range siblings {
			var err error
			before[i], err = EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			plans[i] = Plan(cfg, s, start.At)
		}
		// Start from a shared immutable value so authority operations also prove
		// they cannot mutate slices/maps retained by an approval-enabled sibling.
		active := Apply(approval, ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
		active = Apply(active, pin)
		active = Apply(active, observed)
		active = Apply(active, proposed)
		invalid := observed
		invalid.Binding.EffectiveDigest = "invalid"
		active = Apply(active, invalid)
		if active.AuthorityPin() == nil {
			t.Fatal("authority sibling did not retain its pin")
		}
		for i, s := range siblings {
			after, err := EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before[i], after) || !reflect.DeepEqual(plans[i], Plan(cfg, s, start.At)) || s.authority != nil {
				t.Fatal("authority sibling changed legacy bytes or actions", depth, i)
			}
		}
	}
}
