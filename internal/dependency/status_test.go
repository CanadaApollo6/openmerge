package dependency

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestStatusSelfAcknowledgementIsDistinct follows an opted-in loosening hold
// through self-acknowledgement and revocation. The completed override stays
// visible after the hold clears, with its event reference, and the nonblocking
// warning appears on the hold/status itself only with multiple writers.
func TestStatusSelfAcknowledgementIsDistinct(t *testing.T) {
	f := journalTestFixture(t)
	for _, writers := range [][]string{{"author"}, {"author", "independent"}} {
		t.Run(fmt.Sprintf("writers-%d", len(writers)), func(t *testing.T) {
			multi := len(writers) > 1
			s := applyJournal(t, f.start(t), f.policy(true, writers...))
			policyID := s.lastID
			s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
			s = applyJournal(t, s, f.declaration(t, s))
			d, _ := s.Declaration(f.alias)
			held, ok := s.Status(d.ChangeID, 1, DiagnosticProjection{}, nil)
			if !ok || held.Text != "Dependency declaration requires acknowledgement" || !held.Blocking || held.SelfAcknowledgement != nil || !slices.Contains(held.Details, "an authenticated writer may self-acknowledge under the single-maintainer opt-in") || (len(held.Warnings) == 1) != multi {
				t.Fatalf("held view %+v", held)
			}
			ack := selfAck(ackFor(t, s, f.alias, "author"))
			s = applyJournal(t, s, ack)
			ackID := s.lastID
			v, ok := s.Status(d.ChangeID, 1, DiagnosticProjection{}, nil)
			want := SelfAcknowledgementView{Kind: selfAcknowledgementKind, EventID: ackID, Actor: "author", Overridden: ack.Overridden, GoverningCommit: ack.Acknowledged.GoverningCommit, PolicyDigest: ack.Acknowledged.PolicyDigest}
			if !ok || v.Text != "Dependency declaration self-acknowledged" || v.Blocking || v.Redacted || v.SelfAcknowledgement == nil || fmt.Sprint(*v.SelfAcknowledgement) != fmt.Sprint(want) {
				t.Fatalf("self-acknowledged view %+v", v)
			}
			if (len(v.Warnings) == 1 && v.Warnings[0] == MultiWriterWarning) != multi {
				t.Fatalf("warning %v with %d writers", v.Warnings, len(writers))
			}
			disabled := f.policy(true, writers...)
			disabled.Enabled, disabled.PriorEventID = false, policyID
			disabled.Digest = PolicyDigest(disabled.Policy, false)
			s = applyJournal(t, s, disabled)
			r, ok := s.Status(d.ChangeID, 1, DiagnosticProjection{}, nil)
			if !ok || r.Text != "Dependency declaration requires acknowledgement" || !r.Blocking || r.SelfAcknowledgement == nil || !r.SelfAcknowledgement.Revoked || r.SelfAcknowledgement.EventID != ackID || len(r.Warnings) != 0 {
				t.Fatalf("revoked view %+v", r)
			}
		})
	}
}

// TestStatusIndependentAcknowledgementUnchanged keeps the 5-1 presentation:
// independent acknowledgement of a removed last edge leaves no dependency
// view, and the strict class shows no self-acknowledgement option or warning.
func TestStatusIndependentAcknowledgementUnchanged(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	s = applyJournal(t, s, f.declaration(t, s))
	d, _ := s.Declaration(f.alias)
	held, ok := s.Status(d.ChangeID, 1, DiagnosticProjection{}, nil)
	if !ok || len(held.Warnings) != 0 || held.SelfAcknowledgement != nil || slices.ContainsFunc(held.Details, func(s string) bool { return strings.Contains(s, "self-acknowledge") }) {
		t.Fatalf("strict hold %+v", held)
	}
	s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
	if v, shown := s.Status(d.ChangeID, 1, DiagnosticProjection{}, nil); shown {
		t.Fatalf("independent acknowledgement presented %+v", v)
	}
}

func TestBoundDetails(t *testing.T) {
	var details []string
	for n := range MaxStatusDetails + 3 {
		details = append(details, fmt.Sprintf("line %d", n))
		got := BoundDetails(details)
		if len(details) <= MaxStatusDetails {
			if !slices.Equal(got, details) {
				t.Fatalf("short summary changed: %v", got)
			}
			continue
		}
		omitted := len(details) - MaxStatusDetails + 1
		if len(got) != MaxStatusDetails || !slices.Equal(got[:MaxStatusDetails-1], details[:MaxStatusDetails-1]) || got[MaxStatusDetails-1] != fmt.Sprintf("%d more details in the paginated authorized dependency export", omitted) {
			t.Fatalf("bounded %d: %v", len(details), got)
		}
	}
	if got := BoundDetails(nil); got != nil {
		t.Fatalf("empty summary fabricated %v", got)
	}
}
