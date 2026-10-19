package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLateOriginalSnapshotRoundTrip(t *testing.T) {
	b := approvalBasis()
	old := dismissal(b, 10, forge.TimelineDismissalTime, b.At)
	old.Stance.Head = fmt.Sprintf("%040x", 2)
	s := restart(t, Apply(approvalState(b), old))
	b.Revision = 2
	b.Head = old.Stance.Head
	b.At = b.At.Add(time.Hour)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}})
	s = restart(t, Apply(s, native(b, 10, cp.Approve, old.OriginalSubmission.Time)))
	f, _ := s.ApprovalFacts(b.ChangeID)
	joined := f.Sources[sourceKey(old)]
	if joined.Revision != 2 || joined.Event.Basis.Revision != 2 {
		t.Fatal("join lost consistent attribution", joined)
	}
}

func TestDismissalCannotClearLaterRequestOrCommand(t *testing.T) {
	b := approvalBasis()
	for _, source := range []forge.NativeTimeSource{forge.TimelineDismissalTime, forge.ReviewDismissalTime, forge.ObservationTimeFallback} {
		for _, channel := range []cp.SourceChannel{cp.NativeReview, cp.CommentCommand} {
			original := native(b, 103, cp.Approve, b.At)
			request := native(b, 120, cp.RequestChanges, b.At.Add(10*time.Second))
			if channel == cp.CommentCommand {
				request = command(b, 120, cp.RequestChanges, b.At.Add(10*time.Second))
			}
			d := dismissal(b, 103, source, b.At.Add(30*time.Second))
			permutations([]ReviewStanceObserved{original, request, d}, func(events []ReviewStanceObserved) {
				s := approvalState(b)
				for _, e := range events {
					s = restart(t, Apply(s, e))
				}
				assertKind(t, s, b, cp.RequestChanges)
			})
		}
	}
	// A native dismissal cannot revoke a command-channel approval either.
	s := Apply(approvalState(b), command(b, 120, cp.Approve, b.At))
	s = Apply(s, dismissal(b, 103, forge.TimelineDismissalTime, b.At.Add(time.Minute)))
	assertKind(t, restart(t, s), b, cp.Approve)
}

func TestAmbiguousReusedHeadRequestsStayCurrent(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 1, cp.Approve, b.At))
	for rev := ChangeRevision(2); rev <= 3; rev++ {
		head := fmt.Sprintf("%040x", 2)
		if rev == 3 {
			head = b.Head
		}
		s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: rev, Predecessor: rev - 1, HeadSHA: head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At.Add(time.Duration(rev) * time.Hour)}})
	}
	b.Revision = 3
	b.At = b.At.Add(3 * time.Hour)
	for i, delta := range []time.Duration{-time.Second, 0, 2 * time.Second, 3 * time.Second} {
		for _, kind := range []cp.StanceKind{cp.Approve, cp.RequestChanges} {
			e := native(b, int64(10+i), kind, b.At.Add(delta))
			next := restart(t, Apply(s, e))
			f, _ := next.ApprovalFacts(b.ChangeID)
			want := ChangeRevision(0)
			if kind == cp.RequestChanges {
				want = 3
				assertKind(t, next, b, cp.RequestChanges)
			}
			if f.Sources[sourceKey(e)].Revision != want {
				t.Fatal("nonconservative attribution", kind, delta)
			}
		}
	}
}

func TestSaturationRecoversOnRevisionAndRetainsBarriers(t *testing.T) {
	b := approvalBasis()
	s := approvalState(b)
	f := newApprovalFacts()
	f.HistoryKnown = true
	for i := range maxApprovalSources {
		e := native(b, int64(i+1), cp.Approve, b.At)
		if i > 0 {
			e.Reviewer = person(3)
			e.Stance.UserID = e.Reviewer.ID
			e.Stance.Source.ActingUserID = e.Reviewer.ID
		}
		f.Sources[sourceKey(e)] = AttributedStance{e, 1, b.ReadID}
	}
	recomputeUser(&f, person(2).ID, 1)
	recomputeUser(&f, person(3).ID, 1)
	s.approvalFacts = approvalProjection{b.ChangeID: f}
	blocker := native(b, 5000, cp.RequestChanges, b.At.Add(time.Minute))
	s = restart(t, Apply(s, blocker))
	f, _ = s.ApprovalFacts(b.ChangeID)
	if !f.Saturated || f.Sources[sourceKey(blocker)].Event.Stance.Kind != cp.RequestChanges {
		t.Fatal("positive noise discarded blocker")
	}
	b.Revision = 2
	b.Head = fmt.Sprintf("%040x", 2)
	b.At = b.At.Add(time.Hour)
	s = restart(t, Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}}))
	f, _ = s.ApprovalFacts(b.ChangeID)
	if f.Saturated || len(f.Sources) != 1 {
		t.Fatal("new epoch did not recover", len(f.Sources))
	}
	s = restart(t, Apply(s, native(b, 5001, cp.RequestChanges, b.At.Add(time.Minute))))
	assertKind(t, s, b, cp.RequestChanges)
}

func TestSnapshotRejectsContradictoryApprovalCache(t *testing.T) {
	b := approvalBasis()
	a := native(b, 10, cp.Approve, b.At)
	r := native(b, 20, cp.RequestChanges, b.At.Add(time.Minute))
	s := Apply(Apply(approvalState(b), a), r)
	f := s.approvalFacts[b.ChangeID]
	f.Latest[person(2).ID] = sourceKey(a)
	s.approvalFacts[b.ChangeID] = f
	if _, err := EncodeSnapshot(s); err == nil {
		t.Fatal("encoder persisted contradictory cache")
	}
	// Marshal independently so the decoder sees the forged snapshot.
	s = Apply(Apply(approvalState(b), a), r)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot stateSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	f = snapshot.ApprovalFacts[b.ChangeID]
	f.Latest[person(2).ID] = sourceKey(a)
	snapshot.ApprovalFacts[b.ChangeID] = f
	raw, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeSnapshot(raw); err == nil {
		t.Fatal("decoder trusted contradictory cache")
	}
}

func TestApprovalSnapshotAdmissionBudget(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), eligibilityEvent(b))
	f := s.approvalFacts[b.ChangeID]
	f.Eligibility.Reasons = []string{strings.Repeat("x", maxApprovalChangeBytes)}
	s.approvalFacts[b.ChangeID] = f
	var budget ApprovalSnapshotBudgetError
	if _, err := EncodeSnapshot(s); !errors.As(err, &budget) || budget.ChangeID != b.ChangeID {
		t.Fatal("missing per-change admission rejection", err)
	}
}

func FuzzApprovalReachableSnapshot(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{3, 3, 0, 4, 1, 5, 2, 6, 7, 8, 9, 10, 11, 12})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64 {
			data = data[:64]
		}
		b := approvalBasis()
		s := approvalState(b)
		for i, v := range data {
			at := b.At.Add(time.Duration(int(v%9)-4) * time.Second)
			if v%7 == 6 {
				old := b.Revision
				b.Revision++
				b.Head = fmt.Sprintf("%040x", v%3+1)
				b.At = b.At.Add(time.Hour)
				s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: b.Revision, Predecessor: old, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}})
			} else {
				id := int64(v%16 + 1)
				var e ReviewStanceObserved
				switch v % 7 {
				case 0:
					e = native(b, id, cp.Approve, at)
				case 1:
					e = native(b, id, cp.RequestChanges, at)
				case 2:
					e = command(b, id, cp.Withdraw, at)
				case 3:
					e = dismissal(b, id, forge.TimelineDismissalTime, at)
				case 4:
					e = command(b, id, cp.Approve, at)
				case 5:
					e = dismissal(b, id, forge.ObservationTimeFallback, at)
				}
				e.Basis.ReadID = fmt.Sprint(i % 3)
				s = Apply(s, e)
			}
			s = restart(t, s)
		}
	})
}

func TestApprovalQueueAdmissionBudget(t *testing.T) {
	b := approvalBasis()
	s := State{}
	reason := strings.Repeat("x", 7<<20)
	for i := range 7 {
		id, err := PRChangeID("7", "Owner/Repo", "release", "", 100+i)
		if err != nil {
			t.Fatal(err)
		}
		basis := b
		basis.ChangeID = id
		s = Apply(s, ChangeRevisionObserved{PR: 100 + i, ChangeID: id, Revision: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}})
		e := eligibilityEvent(basis)
		e.Reasons = []string{reason}
		s = Apply(s, e)
	}
	var budget ApprovalSnapshotBudgetError
	if _, err := EncodeSnapshot(s); !errors.As(err, &budget) || budget.QueueBytes <= maxApprovalQueueBytes || budget.ChangeBytes > maxApprovalChangeBytes {
		t.Fatal("queue admission was not rejected per change", err)
	}
}

func TestHeadHistoryDoesNotChangeLegacyRevisionRecords(t *testing.T) {
	b := approvalBasis()
	s := approvalState(b)
	s = Apply(s, PRObserved{12, fmt.Sprintf("%040x", 2), true, true})
	if s.approvalFacts != nil {
		t.Fatal("revision-only queue allocated approvals")
	}
}

func TestR3ApprovalThenOlderReviewDismissedLater(t *testing.T) {
	b := approvalBasis()
	original := native(b, 100, cp.Approve, b.At)
	later := native(b, 120, cp.Approve, b.At.Add(10*time.Second))
	oldDismissal := dismissal(b, 100, forge.TimelineDismissalTime, b.At.Add(time.Minute))
	permutations([]ReviewStanceObserved{original, later, oldDismissal}, func(events []ReviewStanceObserved) {
		state := approvalState(b)
		for _, event := range events {
			state = restart(t, Apply(state, event))
		}
		facts, _ := state.ApprovalFacts(b.ChangeID)
		if facts.Sources[facts.Latest[later.Stance.UserID]].Event.Stance.Source.ID != "120" {
			t.Fatal("older review dismissal revoked later review", facts.Latest)
		}
	})
}
