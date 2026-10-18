package queue

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func permutations(events []ReviewStanceObserved, visit func([]ReviewStanceObserved)) {
	var walk func(int)
	walk = func(i int) {
		if i == len(events) {
			visit(events)
			return
		}
		for j := i; j < len(events); j++ {
			events[i], events[j] = events[j], events[i]
			walk(i + 1)
			events[i], events[j] = events[j], events[i]
		}
	}
	walk(0)
}

func TestStanceSetPermutationsAndEverySnapshotSplit(t *testing.T) {
	b := approvalBasis()
	a := native(b, 10, cp.Approve, b.At)
	r := native(b, 50, cp.RequestChanges, b.At.Add(time.Minute))
	d := dismissal(b, 5, forge.ObservationTimeFallback, b.At.Add(10*time.Second))
	w := command(b, 30, cp.Withdraw, b.At.Add(20*time.Second))
	p := command(b, 80, cp.Approve, b.At.Add(2*time.Minute))
	unknown := native(b, 90, cp.RequestChanges, time.Time{})
	unknown.Stance.Source.UpperTimeBound = time.Time{}
	unknown.SourceTime.UpperBound = time.Time{}
	equal := command(b, 95, cp.Withdraw, r.Stance.Source.UpperTimeBound)
	exact := dismissal(b, 6, forge.TimelineDismissalTime, b.At.Add(10*time.Second))
	cases := []struct {
		events []ReviewStanceObserved
		want   cp.StanceKind
	}{
		{[]ReviewStanceObserved{a, d, r}, cp.RequestChanges},
		{[]ReviewStanceObserved{a, exact, r}, cp.RequestChanges},
		{[]ReviewStanceObserved{a, d, r, w}, cp.RequestChanges},
		{[]ReviewStanceObserved{a, d, r, w, p}, cp.Approve},
		{[]ReviewStanceObserved{a, d, r, p, unknown}, cp.RequestChanges},
		{[]ReviewStanceObserved{a, d, r, w, equal}, cp.RequestChanges},
		{[]ReviewStanceObserved{a, d, r, w, command(b, 99, cp.Withdraw, b.At.Add(2*time.Minute))}, cp.Withdraw},
	}
	// Generated sets vary channel, stance, exact/fallback and missing/equal
	// times. The property is equality of all effective indices and fingerprint,
	// independent of permutation, read grouping and every snapshot split.
	rng := rand.New(rand.NewSource(71))
	for set := 0; set < 12; set++ {
		var events []ReviewStanceObserved
		for i := 0; i < 5; i++ {
			at := b.At.Add(time.Duration(rng.Intn(4)) * time.Second)
			var e ReviewStanceObserved
			switch rng.Intn(5) {
			case 0:
				e = native(b, int64(100+i), cp.Approve, at)
			case 1:
				e = native(b, int64(100+i), cp.RequestChanges, at)
			case 2:
				e = command(b, int64(100+i), cp.Approve, at)
			case 3:
				e = command(b, int64(100+i), cp.Withdraw, at)
			case 4:
				e = dismissal(b, int64(100+i), forge.ObservationTimeFallback, at)
			}
			if rng.Intn(5) == 0 {
				e.Stance.Source.TimeKnown = false
				e.Stance.Source.Precision = ""
				if e.SourceKind == forge.ObservationTimeFallback {
					e.Stance.Source.Precision = cp.ObservationFallback
				}
				e.Stance.Source.UpperTimeBound = time.Time{}
				e.SourceTime.UpperBound = time.Time{}
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			events = append(events, e)
		}
		initial := approvalState(b)
		for _, e := range events {
			initial = Apply(initial, e)
		}
		v, _ := latest(initial, b)
		cases = append(cases, struct {
			events []ReviewStanceObserved
			want   cp.StanceKind
		}{events, v.Event.Stance.Kind})
	}
	runs := 0
	for ci, tc := range cases {
		t.Run(fmt.Sprint(ci), func(t *testing.T) {
			var wantLatest, wantBlockers, wantFrontiers map[string]string
			var fingerprint string
			visited := 0
			permutations(tc.events, func(events []ReviewStanceObserved) {
				visited++
				if testing.Short() && visited > 6 {
					return
				}
				for split := 0; split <= len(events); split++ {
					s := approvalState(b)
					for i, e := range events {
						if i == split {
							s = restart(t, s)
						}
						// Distinct read grouping cannot change the effective stance/barriers.
						e.Basis.ReadID = fmt.Sprint("read-", i%3)
						s = Apply(s, e)
						s = restart(t, s) // Every reachable prefix must encode/decode equally.
						// Duplicate polling neither renews bounds nor needs blockers-first order.
						s = Apply(s, e)
					}
					if split == len(events) {
						s = restart(t, s)
					}
					assertKind(t, s, b, tc.want)
					f, _ := s.ApprovalFacts(b.ChangeID)
					fp := s.ApprovalFingerprint(b.ChangeID)
					if wantLatest == nil {
						wantLatest = f.Latest
						wantBlockers = f.Blockers
						wantFrontiers = f.Frontiers
						fingerprint = fp
					}
					if !reflect.DeepEqual(wantLatest, f.Latest) || !reflect.DeepEqual(wantBlockers, f.Blockers) || !reflect.DeepEqual(wantFrontiers, f.Frontiers) || fp != fingerprint {
						t.Fatalf("divergence split=%d events=%+v", split, events)
					}
					runs++
				}
			})
		})
	}
	t.Logf("sources=3,4,5 cases=%d permutations/snapshot-splits=%d divergences=0", len(cases), runs)
}

func TestHistoricalDismissalCannotEraseReusedHeadRequest(t *testing.T) {
	for _, knownOriginal := range []bool{false, true} {
		for _, source := range []forge.NativeTimeSource{forge.ObservationTimeFallback, forge.TimelineDismissalTime} {
			b := approvalBasis()
			s := approvalState(b)
			if knownOriginal {
				s = Apply(s, native(b, 1, cp.Approve, b.At))
			}
			current := b
			for rev := ChangeRevision(2); rev <= 3; rev++ {
				current.Revision = rev
				current.At = b.At.Add(time.Duration(rev) * time.Hour)
				current.Head = b.Head
				if rev == 2 {
					current.Head = fmt.Sprintf("%040x", 2)
				}
				s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: rev, Predecessor: rev - 1, HeadSHA: current.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: current.At}})
				s = restart(t, s)
			}
			request := native(current, 50, cp.RequestChanges, current.At.Add(time.Minute))
			old := dismissal(current, 1, source, current.At.Add(2*time.Minute))
			for _, events := range [][]ReviewStanceObserved{{old, request}, {request, old}} {
				next := s
				for _, e := range events {
					next = Apply(restart(t, next), e)
				}
				assertKind(t, next, current, cp.RequestChanges)
				f, _ := next.ApprovalFacts(b.ChangeID)
				want := ChangeRevision(0)

				if f.Sources[sourceKey(old)].Revision != want {
					t.Fatal("dismissal attributed using dismissal time")
				}
			}
		}
	}
}

func TestNovelHeadFirstPollAndReusedHeadClockAllowance(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 1, cp.Approve, b.At))
	next := b
	next.Revision = 2
	next.Head = fmt.Sprintf("%040x", 2)
	next.At = b.At.Add(time.Hour)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: next.At}})
	s = Apply(restart(t, s), native(next, 20, cp.Approve, next.At.Add(-time.Minute)))
	assertKind(t, s, next, cp.Approve)
	next.Revision = 3
	next.Head = b.Head
	next.At = next.At.Add(time.Hour)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 3, Predecessor: 2, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: next.At}})
	for i, delta := range []time.Duration{0, 2 * time.Second, 3 * time.Second, 4 * time.Second} {
		e := native(next, int64(30+i), cp.Approve, next.At.Add(delta))
		s = Apply(s, e)
		f, _ := s.ApprovalFacts(b.ChangeID)
		want := ChangeRevision(0)
		if delta > 3*time.Second {
			want = 3
		}
		if f.Sources[sourceKey(e)].Revision != want {
			t.Fatal("clock allowance", delta)
		}
	}
}

func TestAccountabilityPolicyAndIntentBinding(t *testing.T) {
	s, b, current, p, fresh := decisionSetup(t, ownerPool(2))
	account := accountability(b, person(7), person(1), nil)
	account.PolicyDigest = "old-policy"
	s = Apply(s, account)
	if s.evaluateApprovalsTest(p, current, fresh, ownerPool(2)).Allowed {
		t.Fatal("stale mapping authorized own agent's change")
	}
	account.PolicyDigest = current.PolicyDigest
	s = Apply(s, account)
	current.IntentDigest = "stale-intent"
	if s.evaluateApprovalsTest(p, current, fresh, ownerPool(2)).Allowed {
		t.Fatal("unrecorded intent accepted")
	}
	var boundaryError ApprovalBoundaryError
	if !errors.As((State{}).CheckApprovalBoundary(b), &boundaryError) {
		t.Fatal("missing typed rejection")
	}
}

func TestHistoricalSourcesExcludedFromFingerprint(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 10, cp.Approve, b.At))
	fp := s.ApprovalFingerprint(b.ChangeID)
	historical := native(b, 1, cp.Approve, b.At)
	historical.Stance.Head = fmt.Sprintf("%040x", 3)
	s = Apply(s, historical)
	if fp != s.ApprovalFingerprint(b.ChangeID) {
		t.Fatal("historical source changed decision fingerprint")
	}
	s.ConfigDigest = "new-config"
	if fp == s.ApprovalFingerprint(b.ChangeID) {
		t.Fatal("ConfigApplied missing from fingerprint")
	}
}

func BenchmarkApprovalReplay(b *testing.B) {
	for _, size := range []int{1500, maxApprovalSources - 1} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			basis := approvalBasis()
			events := make([]ReviewStanceObserved, size)
			for i := range events {
				at := basis.At.Add(time.Duration(i) * time.Second)
				switch i % 3 {
				case 0:
					events[i] = native(basis, int64(i+1), cp.Approve, at)
				case 1:
					events[i] = native(basis, int64(i+1), cp.RequestChanges, at)
				case 2:
					events[i] = command(basis, int64(i+1), cp.Withdraw, at)
				}
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				s := approvalState(basis)
				for _, e := range events {
					s = Apply(s, e)
				}
				f := s.approvalFacts[basis.ChangeID]
				if f.Saturated || f.Sources[f.Latest[person(2).ID]].Event.Stance.Kind != cp.RequestChanges {
					b.Fatal("replay skipped effective blockers")
				}
				if len(f.Sources) != size {
					b.Fatal("sources lost")
				}
			}
		})
	}
}

func TestFallbackDismissalNeverHidesEligibleRequest(t *testing.T) {
	s, b, current, p, fresh := decisionSetup(t, ownerPool(2))
	p.Approvals.Rules = nil
	p.Approvals.Default = cp.Requirement{Selector: cp.AnyWriter, Count: 2}
	for _, id := range []int64{3, 4} {
		who := person(id)
		fresh.Actors[who.ID] = cp.Actor{Identity: who, Grant: cp.WriteGrant{SubjectID: who.ID, ReturnedID: who.ID, Write: cp.True, Fresh: true}}
		e := native(b, id+100, cp.Approve, b.At.Add(2*time.Minute))
		e.Stance.UserID = who.ID
		e.Stance.Source.ActingUserID = who.ID
		e.Reviewer = who
		s = Apply(s, e)
	}
	d := dismissal(b, 5, forge.ObservationTimeFallback, b.At.Add(10*time.Second))
	r := native(b, 50, cp.RequestChanges, b.At.Add(time.Minute))
	for _, events := range [][]ReviewStanceObserved{{d, r}, {r, d}} {
		next := s
		for _, e := range events {
			next = Apply(restart(t, next), e)
		}
		result, err := next.PrepareApprovalDecision(p, current, fresh)
		if err != nil || result.Evaluation.Allowed || !reflect.DeepEqual(result.Evaluation.BlockingIDs, []string{person(2).ID}) {
			t.Fatal(result.Evaluation, err)
		}
		stored, _ := result.Snapshot.ApprovalFacts(b.ChangeID)
		if !reflect.DeepEqual(stored.Eligibility, &result.Fresh) || result.Accountability == nil || result.Intent == nil || len(result.ReliedOn) != 3 {
			t.Fatal("decision lost exact evaluated inputs")
		}
	}
}

func TestCapacityIsPerChangeAndFailsClosed(t *testing.T) {
	b := approvalBasis()
	s := approvalState(b)
	f := newApprovalFacts()
	for i := range maxApprovalSources {
		e := native(b, int64(i+1), cp.Approve, b.At)
		f.Sources[sourceKey(e)] = AttributedStance{e, 1, b.ReadID}
	}
	recomputeUser(&f, person(2).ID, 1)
	s.approvalFacts = approvalProjection{b.ChangeID: f}
	s = Apply(s, native(b, 5000, cp.Approve, b.At))
	f, _ = s.ApprovalFacts(b.ChangeID)
	if !f.Saturated || len(f.Latest) != 0 || len(f.Blockers) != 0 || len(f.Frontiers) != 0 {
		t.Fatal("saturation retained partial decision")
	}
	other := b
	other.ChangeID, _ = PRChangeID("7", "Owner/Repo", "release", "", 13)
	s = Apply(s, ChangeRevisionObserved{PR: 13, ChangeID: other.ChangeID, Revision: 1, HeadSHA: other.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At.Add(-time.Hour)}})
	s = Apply(s, native(other, 5001, cp.Approve, b.At))
	assertKind(t, s, other, cp.Approve)
}

func TestPriorHeadsSurviveLegacyFirstEnrichment(t *testing.T) {
	b := approvalBasis()
	for _, legacyFirst := range []bool{false, true} {
		s := Apply(approvalState(b), native(b, 1, cp.Approve, b.At))
		for rev := ChangeRevision(2); rev <= 3; rev++ {
			head := fmt.Sprintf("%040x", 2)
			if rev == 3 {
				head = b.Head
			}
			if legacyFirst {
				s = Apply(s, PRObserved{12, head, true, true})
			}
			event := ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: rev, Predecessor: rev - 1, HeadSHA: head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}}
			s = Apply(restart(t, s), event)
			record, _ := s.ApprovalFacts(b.ChangeID)
			if len(record.PriorHeads) != int(rev-1) || record.PriorHeads[0] != b.Head {
				t.Fatal("lost prior head set", record)
			}
			// Returned history must not allow mutation of persisted evidence.
			record.PriorHeads[0] = "tampered"
			again, _ := s.ApprovalFacts(b.ChangeID)
			if again.PriorHeads[0] != b.Head {
				t.Fatal("history alias escaped")
			}
		}
	}
}

func TestMissingSnapshotHeadHistoryStaysUnknown(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 1, cp.Approve, b.At))
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: fmt.Sprintf("%040x", 2), Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}})
	// Model a pre-history snapshot from the earlier identity component.
	record, _ := s.ApprovalFacts(b.ChangeID)
	record.PriorHeads = nil
	record.HistoryKnown = false
	s.approvalFacts[b.ChangeID] = record
	s = restart(t, s)
	next := b
	next.Revision = 3
	next.Head = fmt.Sprintf("%040x", 3)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 3, Predecessor: 2, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: fmt.Sprintf("%040x", 1), At: b.At}})
	e := native(next, 20, cp.Approve, b.At.Add(-time.Minute))
	s = Apply(restart(t, s), e)
	f, _ := s.ApprovalFacts(b.ChangeID)
	if f.Sources[sourceKey(e)].Revision != 0 {
		t.Fatal("partial history became proof of a novel head")
	}
}
