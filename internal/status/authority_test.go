package status

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func authorityEvent(t *testing.T, name string) queue.Event {
	t.Helper()
	raw, err := os.ReadFile("../queue/testdata/wire/v1/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestAuthorityRenderingAndRestart(t *testing.T) {
	pin := authorityEvent(t, "event_policy_authority_applied").(queue.PolicyAuthorityApplied)
	verified := authorityEvent(t, "event_write_restriction_verified").(queue.WriteRestrictionVerified)
	verified.Scope = "applied"
	seed := queue.Apply(queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}, queue.ConfigApplied{Commit: pin.GoverningCommit, Digest: pin.EffectiveDigest})
	base := queue.Apply(seed, pin)
	now := verified.VerifiedAt.Add(time.Hour)
	seen := queue.WriteRestrictionObserved{Binding: verified.Binding, Basis: verified.Basis, ObservedAt: verified.VerifiedAt.Add(10 * time.Minute), Evidence: authority.CloneObservation(verified.Evidence)}
	hidden := seen
	hidden.Evidence = authority.CloneObservation(seen.Evidence)
	hidden.Evidence.Protections[0].BypassVisibility = authority.Hidden
	hidden.Evidence.Protections[0].BypassActors = []authority.BypassActor{}
	violated := seen
	violated.Evidence = authority.CloneObservation(seen.Evidence)
	violated.Evidence.Findings = []authority.Finding{{Element: "bypass", Kind: "missing_rule", ProtectionID: "repository_ruleset/base-10"}}
	if err := violated.Validate(); err != nil {
		t.Fatal(err)
	}
	sticky := hidden
	sticky.Evidence = authority.CloneObservation(hidden.Evidence)
	sticky.Evidence.Protections[0].RulesVisibility = authority.Hidden
	sticky.Evidence.Protections[0].Rules = []authority.Rule{}
	for _, tc := range []struct {
		name, want string
		events     []queue.Event
	}{
		{"never", "unverified", nil},
		{"observed only", "verified", []queue.Event{seen}},
		{"verified", "verified", []queue.Event{verified, seen}},
		{"hidden", "unverified", []queue.Event{verified, hidden}},
		{"violated", "violated", []queue.Event{verified, violated}},
		{"hidden retains violation", "violated", []queue.Event{verified, violated, sticky}},
		{"repaired", "verified", []queue.Event{verified, violated, queue.WriteRestrictionObserved{Binding: seen.Binding, Basis: seen.Basis + 1, ObservedAt: seen.ObservedAt.Add(time.Minute), Evidence: seen.Evidence}}},
		{"stale observation", "verified", []queue.Event{verified, seen, queue.WriteRestrictionObserved{Binding: hidden.Binding, Basis: 0, ObservedAt: hidden.ObservedAt.Add(-time.Hour), Evidence: hidden.Evidence}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			for i, e := range tc.events {
				switch v := e.(type) {
				case queue.WriteRestrictionVerified:
					v.Basis = s.RulesSourceRevision(queue.WriteRestriction)
					e = v
				case queue.WriteRestrictionObserved:
					if tc.name != "stale observation" || i != len(tc.events)-1 {
						v.Basis = s.RulesSourceRevision(queue.WriteRestriction)
					}
					e = v
				}
				s = queue.Apply(s, e)
			}
			a := ProjectAuthority(s, now)
			if a == nil || a.State != tc.want {
				t.Fatalf("%+v", a)
			}
			before, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := queue.EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := queue.DecodeSnapshot(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(ProjectAuthority(restored, now))
			if string(before) != string(after) {
				t.Fatalf("restart changed authority: %s / %s", before, after)
			}
			text := AuthoritySummary(a)
			if !strings.Contains(text, "authority "+tc.want) || !strings.Contains(text, "last observed:") || !strings.Contains(text, "last verified:") {
				t.Fatal(text)
			}
			if tc.name == "never" && (a.LastObservedAge != nil || a.LastVerifiedAge != nil || !strings.Contains(text, "never (age unknown)")) {
				t.Fatal(text)
			}
			if tc.name == "verified" && (*a.LastObservedAge != 3000 || *a.LastVerifiedAge != 3600) {
				t.Fatalf("ages conflated: %s", before)
			}
			if tc.name == "hidden" && !strings.Contains(text, "hidden") {
				t.Fatal(text)
			}
			health := EvaluateHealth(HealthQueue{Watched: true, StartedAt: now, Authority: a}, DefaultHealthSettings(), now)
			if health.Failing() != (tc.want == "violated") {
				t.Fatal("health disagrees with status")
			}
			j := store.Journal{Seed: s}
			q, _, _ := Project(j, 0, 0, nil)
			if q.Authority == nil || q.Authority.State != tc.want {
				t.Fatal("API projection disagrees")
			}
		})
	}
	legacy := queue.State{}
	if ProjectAuthority(legacy, now) != nil {
		t.Fatal("legacy acquired authority")
	}
	proposed := queue.Apply(base, authorityEvent(t, "event_write_restriction_verified"))
	if ProjectAuthority(proposed, now).LastVerifiedAt != nil {
		t.Fatal("proposed verification became applied")
	}
}

func TestAuthorityFutureEvidenceHasExplicitClockSkew(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	a := &Authority{LastObservedAt: &future, LastVerifiedAt: &past}
	a.RefreshAges(now)
	if !a.ClockSkew || !a.LastObservedClockSkew || a.LastVerifiedClockSkew || a.LastObservedAge != nil || a.LastVerifiedAge == nil || *a.LastVerifiedAge != 60 {
		t.Fatalf("%+v", a)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"clock_skew":true`) || !strings.Contains(string(raw), `"last_observed_age_seconds":null`) || !strings.Contains(AuthoritySummary(a), "clock_skew") {
		t.Fatalf("%s / %s", raw, AuthoritySummary(a))
	}
	a.RefreshAges(future.Add(time.Second))
	if a.ClockSkew || a.LastObservedClockSkew || a.LastObservedAge == nil {
		t.Fatal("skew marker failed to clear")
	}
}
