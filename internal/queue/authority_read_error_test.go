package queue

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
)

func TestAuthorityReadErrorSourceIsolationAndRepair(t *testing.T) {
	s, v := restrictionState(t)
	at := v.VerifiedAt
	s = restrictionRead(s, v, at, v.Evidence)
	s = Apply(s, RulesSourceBlocked{Source: Permissions, Problems: []string{"permission missing"}})
	status := s.WriteRestrictionStatus()
	failure := WriteRestrictionObserved{Binding: v.Binding, Basis: s.RulesSourceRevision(WriteRestriction), ObservedAt: at.Add(time.Second), ReadError: "write restriction read failed: permission unavailable", Evidence: authority.Observation{Inventory: authority.ReadFailure, Protections: []authority.Protection{}, Coverage: []authority.Coverage{}, Findings: []authority.Finding{}}}
	_, raw, err := EncodeEvent(failure)
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	s = Apply(s, event)
	if got := s.WriteRestrictionStatus(); got.State != "unverified" || !got.LastObservedAt.Equal(status.LastObservedAt) || !got.LastVerifiedAt.Equal(status.LastVerifiedAt) {
		t.Fatalf("failure refreshed successful ages or passed: %+v", got)
	}
	if len(s.RulesSourceProblems(WriteRestriction)) != 1 || !reflect.DeepEqual(s.RulesSourceProblems(Permissions), []string{"permission missing"}) {
		t.Fatal("failure changed unrelated source")
	}
	// A repair read started before the error was recorded cannot clear it.
	s = Apply(s, WriteRestrictionObserved{Binding: v.Binding, Basis: failure.Basis, ObservedAt: failure.ObservedAt.Add(time.Second), Evidence: v.Evidence})
	if s.WriteRestrictionStatus().State != "unverified" || len(s.RulesSourceProblems(WriteRestriction)) != 1 {
		t.Fatal("stale repair cleared read failure")
	}
	restored, err := DecodeSnapshot(snapshotBytes(t, s))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.WriteRestrictionStatus(), restored.WriteRestrictionStatus()) || !reflect.DeepEqual(s.RulesSources(), restored.RulesSources()) {
		t.Fatal("restart lost read failure")
	}
	s = restrictionRead(restored, v, at.Add(3*time.Second), v.Evidence)
	if s.WriteRestrictionStatus().State != "verified" || len(s.RulesSourceProblems(WriteRestriction)) != 0 || !reflect.DeepEqual(s.RulesSourceProblems(Permissions), []string{"permission missing"}) {
		t.Fatal("fresh repair did not isolate its clear")
	}
	// Rollback removes the read requirement itself and preserves other findings.
	failure.Basis = s.RulesSourceRevision(WriteRestriction)
	failure.ObservedAt = at.Add(4 * time.Second)
	s = Apply(s, failure)
	pin := s.AuthorityPin()
	pin.Authority = authority.GitHub
	pin.OpenMerge = nil
	s = Apply(s, *pin)
	if len(s.RulesSourceProblems(WriteRestriction)) != 0 || !reflect.DeepEqual(s.RulesSourceProblems(Permissions), []string{"permission missing"}) {
		t.Fatal("rollback changed unrelated source")
	}
	for _, bad := range []WriteRestrictionObserved{
		{Binding: v.Binding, Basis: 0, ObservedAt: at, Evidence: v.Evidence, ReadError: "invented failure on successful evidence"},
		{Binding: v.Binding, Basis: 0, ObservedAt: at, Evidence: failure.Evidence, ReadError: strings.Repeat("x", 1025)},
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid read error accepted")
		}
	}
}
