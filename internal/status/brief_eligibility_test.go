package status

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func eligibilityJournal(t *testing.T) (store.Journal, cp.Policy, queue.ApprovalEligibilityObserved) {
	t.Helper()
	j, _, policy, b := policyJournal(t, true)
	j.Events = j.Events[:len(j.Events)-1] // Remove the fixture's later config change.
	who := cp.Identity{ID: "github.com:user:2", Login: "reviewer", Kind: cp.HumanAccount}
	review, ok := queue.NativeStance(b, forge.NativeReview{ID: 5577526295, CommitID: b.Head, Author: forge.UserIdentity{ID: 2, Login: who.Login, Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: b.At.Add(-time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}})
	if !ok {
		t.Fatal("invalid review")
	}
	appendEligibilityEvents(&j, review)
	e := queue.ApprovalEligibilityObserved{Basis: b, Actors: map[string]cp.Actor{who.ID: {Identity: who, Grant: cp.WriteGrant{SubjectID: who.ID, ReturnedID: who.ID, Fresh: true, Write: cp.True}}}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}, Known: true, Reasons: []string{}}
	return j, policy, e
}

func appendEligibilityEvents(j *store.Journal, events ...queue.Event) {
	for _, e := range events {
		j.Events = append(j.Events, store.JournalEvent{ID: int64(len(j.Events) + 1), Event: e})
	}
}

func projectedEligibility(t *testing.T, j store.Journal, policy cp.Policy) brief.View {
	t.Helper()
	q, _, _ := Project(j, 0, 0, nil)
	id, _ := j.Key.ChangeID(12)
	facts, _ := q.state.ApprovalFacts(id)
	f, err := q.state.RecordedBrief(policy, facts.Intent.Basis)
	if err != nil {
		t.Fatal(err)
	}
	f = recordedBriefProgress(f, policy, q.state, facts, q.eligibilityPins[id])
	v, err := brief.Project(f, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBriefEligibilityCommitOrder(t *testing.T) {
	j, p, e := eligibilityJournal(t)
	pending := projectedEligibility(t, j, p)
	if pending.Obligations[0].Code != "eligibility_pending" || pending.Obligations[0].Met {
		t.Fatal(pending.Obligations)
	}
	e.Basis.At = e.Basis.At.Add(time.Hour) // The first worker's clock runs ahead.
	appendEligibilityEvents(&j, e)
	met := projectedEligibility(t, j, p)
	if !met.Obligations[0].Met || !reflect.DeepEqual(met.Obligations[0].ApprovedIDs, []string{"github.com:user:2"}) {
		t.Fatal(met.Obligations)
	}
	denial := e
	denial.Basis.ReadID = "later-denial"
	denial.Basis.At = e.Basis.At.Add(-time.Hour)
	denial.Actors = map[string]cp.Actor{}
	for id, actor := range e.Actors {
		actor.Grant.Write = cp.False
		denial.Actors[id] = actor
	}
	appendEligibilityEvents(&j, denial)
	denied := projectedEligibility(t, j, p)
	if denied.Obligations[0].Met || denied.Obligations[0].Code != "approval_required" || len(denied.Obligations[0].ApprovedIDs) != 0 {
		t.Fatal(denied.Obligations)
	}
	unknown := denial
	unknown.Basis.ReadID = "later-unknown"
	unknown.Known = false
	unknown.Reasons = []string{"read unavailable"}
	appendEligibilityEvents(&j, unknown)
	unresolved := projectedEligibility(t, j, p)
	if unresolved.Obligations[0].Code != "unknown_eligibility" || unresolved.Obligations[0].Met {
		t.Fatal(unresolved.Obligations)
	}
	for _, view := range []brief.View{met, denied, unresolved} {
		if view.Digest != pending.Digest {
			t.Fatal("eligibility changed digest")
		}
	}
}

func TestBriefEligibilityStaleBindings(t *testing.T) {
	for _, change := range []string{"revision", "head", "policy", "config", "config-reused", "generation"} {
		t.Run(change, func(t *testing.T) {
			j, p, e := eligibilityJournal(t)
			appendEligibilityEvents(&j, e)
			if !projectedEligibility(t, j, p).Obligations[0].Met {
				t.Fatal("fixture not approved")
			}
			classified := j.Events[2].Event.(queue.ChangeClassified)
			account := j.Events[3].Event.(queue.ChangeAccountabilityObserved)
			intent := j.Events[4].Event.(queue.ChangeIntentObserved)
			b := e.Basis
			switch change {
			case "revision", "head":
				b.Revision++
				if change == "revision" {
					appendEligibilityEvents(&j, queue.ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: strings.Repeat("e", 40), Basis: queue.RevisionObservationBasis{BaseSHA: classified.Evidence.Classification.Inputs.Base, At: b.At}})
					b.Revision++ // Reuse the original head in a later durable revision.
				} else {
					b.Head = strings.Repeat("e", 40)
				}
				c := &classified.Evidence.Classification.Inputs
				c.Revision, c.Head, c.Generation = uint64(b.Revision), b.Head, 1
				appendEligibilityEvents(&j, queue.ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: b.Revision, Predecessor: b.Revision - 1, HeadSHA: b.Head, Basis: queue.RevisionObservationBasis{BaseSHA: c.Base, At: b.At}})
			case "policy":
				p.Digest = strings.Repeat("e", 64)
				classified.Evidence.Classification.Inputs.PolicyDigest = p.Digest
				classified.Evidence.Classification.Inputs.Generation++
				account.PolicyDigest = p.Digest
			case "config", "config-reused":
				appendEligibilityEvents(&j, queue.ConfigApplied{Commit: strings.Repeat("e", 40), Digest: strings.Repeat("e", 64)})
				if change == "config-reused" {
					appendEligibilityEvents(&j, j.Events[0].Event)
				}
			case "generation":
				classified.Evidence.Classification.Inputs.Generation++
			}
			account.Basis, intent.Basis = b, b
			appendEligibilityEvents(&j, classified, account, intent)
			stale := projectedEligibility(t, j, p)
			if stale.Obligations[0].Code != "eligibility_stale" || stale.Obligations[0].Met || len(stale.Obligations[0].ApprovedIDs) != 0 {
				t.Fatal(stale.Obligations)
			}
			// A rejected replay of an old input-change event cannot renew the policy pin.
			appendEligibilityEvents(&j, queue.ApprovalInputsChanged{Basis: e.Basis, Eligibility: e, PreviousFingerprint: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64)})
			if got := projectedEligibility(t, j, p); got.Obligations[0].Code != "eligibility_stale" {
				t.Fatal(got.Obligations)
			}
		})
	}
}

func TestBriefEligibilityBaseRewriteWindow(t *testing.T) {
	j, p, e := eligibilityJournal(t)
	appendEligibilityEvents(&j, e)
	before := projectedEligibility(t, j, p)
	if !before.Obligations[0].Met {
		t.Fatal("fixture not approved")
	}
	// BaseMoved has no ancestry metadata. Until reclassification, the brief
	// remains explicitly bound to the old base and generation.
	newBase := strings.Repeat("e", 40)
	appendEligibilityEvents(&j, queue.BaseMoved{SHA: newBase})
	window := projectedEligibility(t, j, p)
	if !window.Obligations[0].Met || window.Classification.Base != before.Classification.Base || window.Classification.Generation != before.Classification.Generation || window.Digest != before.Digest {
		t.Fatalf("base move relabeled old approval evidence: %+v", window)
	}
	classified := j.Events[2].Event.(queue.ChangeClassified)
	classified.Evidence.Classification.Inputs.Base = newBase
	classified.Evidence.Classification.Inputs.Generation++
	appendEligibilityEvents(&j, classified)
	after := projectedEligibility(t, j, p)
	if after.Classification.Base != newBase || after.Classification.Generation != before.Classification.Generation+1 || after.Obligations[0].Met || after.Obligations[0].Code != "eligibility_stale" || len(after.Obligations[0].ApprovedIDs) != 0 {
		t.Fatalf("old eligibility credited to rewritten base: %+v", after)
	}
}

func TestBriefEligibilityReevaluatesLaterStances(t *testing.T) {
	j, p, e := eligibilityJournal(t)
	review := j.Events[len(j.Events)-1].Event.(queue.ReviewStanceObserved)
	j.Events = j.Events[:len(j.Events)-1]
	appendEligibilityEvents(&j, e)
	before := projectedEligibility(t, j, p)
	if before.Obligations[0].Met {
		t.Fatal("approval not yet recorded")
	}
	appendEligibilityEvents(&j, review)
	approved := projectedEligibility(t, j, p)
	if !approved.Obligations[0].Met || !reflect.DeepEqual(approved.Obligations[0].ApprovedIDs, []string{review.Reviewer.ID}) {
		t.Fatal(approved.Obligations)
	}
	withdrawal := review
	withdrawal.Stance.Kind = cp.Withdraw
	withdrawal.Stance.Source.DismissedReviewID = review.Stance.Source.ID
	withdrawal.OriginalSubmission = review.SourceTime
	withdrawal.SourceKind = forge.ReviewDismissalTime
	withdrawal.SourceTime.Source = forge.ReviewDismissalTime
	withdrawal.Stance.Source.ID = "5577526296"
	withdrawal.Stance.Source.Sequence++
	withdrawal.Stance.Source.Time = review.Stance.Source.Time.Add(time.Minute)
	withdrawal.SourceTime.Time = withdrawal.Stance.Source.Time
	if err := withdrawal.Validate(); err != nil {
		t.Fatal(err)
	}
	appendEligibilityEvents(&j, withdrawal)
	withdrawn := projectedEligibility(t, j, p)
	if withdrawn.Obligations[0].Met || len(withdrawn.Obligations[0].ApprovedIDs) != 0 {
		t.Fatal(withdrawn.Obligations)
	}
	if approved.Digest != before.Digest || withdrawn.Digest != before.Digest {
		t.Fatal("stance progress changed receipt identity")
	}
}
