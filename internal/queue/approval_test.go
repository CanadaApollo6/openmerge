package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func approvalBasis() ApprovalBasis {
	id, _ := PRChangeID("7", "Owner/Repo", "release", "", 12)
	return ApprovalBasis{id, 1, strings.Repeat("b", 40), "read-1", time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
}
func person(n int64) cp.Identity {
	return cp.Identity{ID: classification.GitHubUserID(n), Login: fmt.Sprint("user", n), Kind: cp.HumanAccount}
}
func approvalState(b ApprovalBasis) State {
	return Apply(State{}, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: b.At.Add(-time.Hour)}})
}
func native(b ApprovalBasis, id int64, kind cp.StanceKind, at time.Time) ReviewStanceObserved {
	r := forge.NativeReview{ID: id, CommitID: b.Head, Author: forge.UserIdentity{ID: 2, Login: "reviewer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: at, Source: forge.ReviewSubmissionTime, Precision: time.Second, UpperBound: at.Add(time.Second)}, Observation: forge.NativeObservation{ObservedAt: b.At, ResponseDate: b.At}}
	if kind == cp.RequestChanges {
		r.State = forge.ReviewChangesRequested
	}
	e, ok := NativeStance(b, r)
	if !ok {
		panic("bad test review")
	}
	return e
}
func command(b ApprovalBasis, id int64, kind cp.StanceKind, at time.Time) ReviewStanceObserved {
	e := native(b, id, cp.Approve, at)
	e.Stance.Kind = kind
	e.Stance.Source.Channel = cp.CommentCommand
	e.SourceKind = forge.CommentCreationTime
	e.SourceTime.Source = e.SourceKind
	if kind == cp.Approve {
		e.Stance.Receipt = &cp.ReceiptIdentity{ChangeID: string(b.ChangeID), Revision: uint64(b.Revision), Generation: 1, Base: strings.Repeat("a", 40), Head: b.Head, MergeBase: strings.Repeat("c", 40), IntentDigest: "intent", PolicyDigest: "semantic-policy", BriefDigest: "brief"}
	}
	return e
}
func dismissal(b ApprovalBasis, id int64, source forge.NativeTimeSource, at time.Time) ReviewStanceObserved {
	r := forge.NativeReview{ID: id, CommitID: b.Head, Author: forge.UserIdentity{ID: 2, Login: "reviewer", Kind: forge.UserAccount}, State: forge.ReviewDismissed, Submitted: forge.NativeTime{Time: at.Add(-24 * time.Hour), Source: forge.ReviewSubmissionTime}, Dismissal: &forge.NativeDismissal{ReviewID: id, Actor: forge.UserIdentity{ID: 9, Login: "maintainer", Kind: forge.UserAccount}, Time: forge.NativeTime{Time: at, Source: source, Precision: time.Second, Uncertainty: 2 * time.Second, UpperBound: at.Add(3 * time.Second)}}}
	e, ok := NativeStance(b, r)
	if !ok {
		panic("bad test dismissal")
	}
	return e
}
func latest(s State, b ApprovalBasis) (AttributedStance, bool) {
	f, _ := s.ApprovalFacts(b.ChangeID)
	key, ok := f.Latest[person(2).ID]
	return f.Sources[key], ok
}
func restart(t *testing.T, s State) State {
	t.Helper()
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return restored
}
func assertKind(t *testing.T, s State, b ApprovalBasis, kind cp.StanceKind) {
	t.Helper()
	v, ok := latest(s, b)
	if !ok || v.Event.Stance.Kind != kind {
		t.Fatalf("want %s, got %+v", kind, v)
	}
}
func eligibilityEvent(b ApprovalBasis) ApprovalEligibilityObserved {
	who := person(2)
	who.Login = "reviewer"
	return ApprovalEligibilityObserved{Basis: b, Actors: map[string]cp.Actor{who.ID: {Identity: who, Grant: cp.WriteGrant{SubjectID: who.ID, ReturnedID: who.ID, Fresh: true, Write: cp.True}}}, Groups: map[string]cp.IdentitySet{}, OwnerPools: map[string]cp.IdentitySet{}, Known: true, Reasons: []string{}}
}

func TestApprovalWireFixturesAndValidation(t *testing.T) {
	b := approvalBasis()
	account := Accountability(b, person(7), person(1), nil)
	approval := native(b, 10, cp.Approve, b.At.Add(-time.Minute))
	unknown := eligibilityEvent(b)
	unknown.Known = false
	unknown.Reasons = []string{"membership unavailable"}
	mapped := Accountability(b, person(7), person(1), map[string][]cp.Identity{person(1).ID: {person(3)}})
	bot := person(4)
	bot.Kind = cp.AgentAccount
	vectors := map[string]Event{"person": account, "mapped-agent": mapped, "unmapped-bot": Accountability(b, person(7), bot, nil), "approval": approval, "changes-requested": native(b, 11, cp.RequestChanges, b.At), "dismissal": dismissal(b, 10, forge.ObservationTimeFallback, b.At), "withdrawal": command(b, 12, cp.Withdraw, b.At), "unknown-eligibility": unknown}
	emptyEligibility := eligibilityEvent(b)
	emptyEligibility.Actors = map[string]cp.Actor{}
	registry := map[string]Event{"change_accountability_observed": account, "change_intent_observed": Intent(b, " Title\r\n", "Body\r\ntext "), "review_stance_observed": approval, "approval_eligibility_observed": emptyEligibility}
	for name, event := range registry {
		vectors["event_"+name] = event
	}
	for name, event := range vectors {
		_, raw, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(name, err)
		}
		directory := "testdata/approvals"
		if strings.HasPrefix(name, "event_") {
			directory = "testdata/wire/v1"
		}
		path := filepath.Join(directory, name+".json")
		if os.Getenv("UPDATE_APPROVAL_FIXTURES") == "1" {
			if err := os.MkdirAll(directory, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(want, raw) {
			t.Fatal(name, "fixture drift", err)
		}
		decoded, err := DecodeEvent(raw)
		if err != nil || !reflect.DeepEqual(event, decoded) {
			t.Fatal(name, "round trip", err)
		}
		var envelope map[string]any
		_ = json.Unmarshal(raw, &envelope)
		for _, mutate := range []func(map[string]any){func(v map[string]any) { delete(v, "basis") }, func(v map[string]any) { v["extra"] = true }, func(v map[string]any) { v["basis"].(map[string]any)["revision"] = 0 }, func(v map[string]any) { delete(v["basis"].(map[string]any), "head") }, func(v map[string]any) { v["basis"].(map[string]any)["extra"] = true }} {
			mutated := detached(envelope)
			mutate(mutated["data"].(map[string]any))
			bad, _ := json.Marshal(mutated)
			if _, err := DecodeEvent(bad); err == nil {
				t.Fatal("accepted malformed", string(bad))
			}
		}
	}
	bad := approval
	bad.Stance.UserID = "github.com:user:02"
	if bad.Validate() == nil {
		t.Fatal("noncanonical user ID")
	}
	bad = approval
	bad.Stance.Receipt = command(b, 12, cp.Approve, b.At).Stance.Receipt
	if bad.Validate() == nil {
		t.Fatal("native intent inferred")
	}
}

func TestStanceBlockersWinPermutationsTiesAndMissingTimes(t *testing.T) {
	b := approvalBasis()
	for _, block := range []ReviewStanceObserved{native(b, 20, cp.RequestChanges, b.At), command(b, 20, cp.Withdraw, b.At), dismissal(b, 20, forge.ReviewDismissalTime, b.At), dismissal(b, 20, forge.TimelineDismissalTime, b.At), dismissal(b, 20, forge.ObservationTimeFallback, b.At)} {
		for _, missing := range []bool{false, true} {
			for _, channel := range []cp.SourceChannel{cp.NativeReview, cp.CommentCommand} {
				for _, reverse := range []bool{false, true} {
					blocker := detached(block)
					positive := native(b, 10, cp.Approve, b.At)
					if channel == cp.CommentCommand {
						positive = command(b, 10, cp.Approve, b.At)
					}
					if missing {
						positive.Stance.Source.TimeKnown = false
						positive.Stance.Source.Precision = ""
					}
					events := []Event{positive, blocker}
					if reverse {
						events = []Event{blocker, positive}
					}
					s := approvalState(b)
					for _, e := range events {
						s = Apply(s, e)
					}
					assertKind(t, s, b, blocker.Stance.Kind)
					s = restart(t, s)
					s = Apply(s, positive)
					assertKind(t, s, b, blocker.Stance.Kind)
				}
			}
		}
	}
}

func TestDismissalFallbackFirstObservationRecoveryAndRestart(t *testing.T) {
	b := approvalBasis()
	for _, source := range []forge.NativeTimeSource{forge.ReviewDismissalTime, forge.TimelineDismissalTime, forge.ObservationTimeFallback} {
		for _, withBound := range []bool{true, false} {
			blocker := dismissal(b, 10, source, b.At)
			if !withBound {
				blocker.Stance.Source.TimeKnown = false
				blocker.Stance.Source.Precision = cp.ObservationFallback
				blocker.Stance.Source.UpperTimeBound = time.Time{}
				blocker.SourceTime.UpperBound = time.Time{}
			}
			s := Apply(approvalState(b), blocker)
			original := s
			fp := s.ApprovalFingerprint(b.ChangeID)
			poll := detached(blocker)
			poll.Basis.ReadID = "read-2"
			poll.SourceTime.Time = b.At.Add(time.Hour)
			poll.SourceTime.UpperBound = b.At.Add(2 * time.Hour)
			poll.Stance.Source.Time = poll.SourceTime.Time
			poll.Stance.Source.UpperTimeBound = poll.SourceTime.UpperBound
			poll.Stance.Source.ID = "999"
			poll.Stance.Source.Sequence = 999
			s = Apply(restart(t, s), poll)
			if s.ApprovalFingerprint(b.ChangeID) != fp {
				t.Fatal("poll renewed barrier/fingerprint")
			}
			first, _ := latest(s, b)
			if first.Event.Stance.Source.UpperTimeBound != blocker.Stance.Source.UpperTimeBound {
				t.Fatal("barrier moved")
			}
			equal := native(poll.Basis, 30, cp.Approve, blocker.Stance.Source.UpperTimeBound)
			s = Apply(s, equal)
			assertKind(t, s, b, cp.Withdraw)
			fresh := native(poll.Basis, 31, cp.Approve, b.At.Add(4*time.Second))
			s = Apply(restart(t, s), fresh)
			if withBound {
				assertKind(t, s, b, cp.Approve)
			} else {
				assertKind(t, s, b, cp.Withdraw)
			}
			assertKind(t, original, b, cp.Withdraw)
		}
	}
}

func TestSourceOrderingAndCrossChannelLatest(t *testing.T) {
	b := approvalBasis()
	older := native(b, 10, cp.Approve, b.At.Add(-time.Minute))
	newer := command(b, 20, cp.Approve, b.At)
	for _, events := range [][]Event{{older, newer, older}, {newer, older, newer}} {
		s := approvalState(b)
		for _, e := range events {
			s = Apply(s, e)
		}
		v, _ := latest(s, b)
		if v.Event.Stance.Source.Channel != cp.CommentCommand {
			t.Fatal("older positive resurrected")
		}
	}
	// Independent IDs are ordered within their channel, even at equal times.
	s := Apply(Apply(approvalState(b), native(b, 20, cp.RequestChanges, b.At)), native(b, 10, cp.Approve, b.At.Add(time.Hour)))
	assertKind(t, s, b, cp.RequestChanges)
	// A latest native stance never acquires the receipt's intent binding.
	s = Apply(Apply(approvalState(b), command(b, 10, cp.Approve, b.At)), native(b, 20, cp.Approve, b.At.Add(time.Minute)))
	v, _ := latest(s, b)
	if v.Event.Stance.Receipt != nil {
		t.Fatal("native copied receipt")
	}
}

func TestHeadReuseAttributionAndSnapshotTail(t *testing.T) {
	b := approvalBasis()
	old := native(b, 10, cp.Approve, b.At.Add(-time.Minute))
	for _, previouslyObserved := range []bool{false, true} {
		for _, withRestart := range []bool{false, true} {
			s := approvalState(b)
			if previouslyObserved {
				s = Apply(s, old)
			}
			for revision := ChangeRevision(2); revision <= 3; revision++ {
				head := strings.Repeat("c", 40)
				if revision == 3 {
					head = b.Head
				}
				s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: revision, Predecessor: revision - 1, HeadSHA: head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: b.At.Add(time.Duration(revision) * time.Hour)}})
				if withRestart {
					s = restart(t, s)
				}
			}
			current := b
			current.Revision = 3
			current.At = b.At.Add(4 * time.Hour)
			late := old
			late.Basis = current
			late.Stance.Revision = 3
			before := s
			s = Apply(s, late)
			restored := Apply(restart(t, before), late)
			if !reflect.DeepEqual(s.approvalFacts, restored.approvalFacts) {
				t.Fatal("snapshot tail attribution differs")
			}
			f, _ := s.ApprovalFacts(b.ChangeID)
			attr := f.Sources[sourceKey(late)]
			if attr.Revision == 3 {
				t.Fatal("head equality authorized old review")
			}
			fresh := native(current, 30, cp.Approve, current.At)
			s = Apply(restart(t, s), fresh)
			v, _ := latest(s, current)
			if v.Revision != 3 {
				t.Fatal("fresh current review failed recovery")
			}
		}
	}
}

func decisionSetup(t *testing.T, resolve classification.OwnerResolver) (State, ApprovalBasis, cp.ReceiptIdentity, cp.Policy, ApprovalEligibilityObserved) {
	t.Helper()
	b := approvalBasis()
	e := classificationEvent(t, classification.TreeDeriver{Resolve: resolve})
	evidence := e.Evidence
	if e.Validate() != nil {
		t.Fatal(e.Validate())
	}
	s := Apply(approvalState(b), e)
	s = Apply(s, Accountability(b, person(7), person(1), nil))
	s = Apply(s, native(b, 10, cp.Approve, b.At))
	i := evidence.Classification.Inputs
	current := cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest, IntentDigest: "intent", BriefDigest: "brief"}
	p := cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.AnyWriter, Count: 1}, Rules: []cp.Rule{{When: cp.Match{Touches: []string{"docs/**"}}, Require: cp.Requirement{Selector: cp.CodeOwners}}}}}
	return s, b, current, p, eligibilityEvent(b)
}
func ownerPool(ids ...int64) classification.OwnerResolver {
	return func(classification.Owner) cp.IdentitySet {
		pool := cp.IdentitySet{Resolved: true, Fresh: true}
		for _, id := range ids {
			pool.IDs = append(pool.IDs, classification.GitHubUserID(id))
		}
		return pool
	}
}
func TestDecisionNeverConsumesStoredFreshOwnerPools(t *testing.T) {
	s, b, current, policy, fresh := decisionSetup(t, ownerPool(2))
	stored, _ := s.Classification(b.ChangeID, 1)
	if !stored.Evidence.Classification.CodeOwners[0].Eligible.Fresh {
		t.Fatal("test requires stored Fresh=true")
	}
	if !s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)).Allowed {
		t.Fatal("fresh member should approve")
	}
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(9)); e.Allowed || len(e.ReviewLogins) > 0 {
		t.Fatal("removed member consumed stored Fresh=true pool", e)
	}
	if e := s.evaluateApprovalsTest(policy, current, fresh, nil); e.Allowed || e.ReviewsKnown {
		t.Fatal("unavailable resolution became empty known membership", e)
	}
	wrong := func(classification.Owner) cp.IdentitySet {
		return cp.IdentitySet{IDs: []string{"github.com:user:02"}, Resolved: true, Fresh: true}
	}
	if e := s.evaluateApprovalsTest(policy, current, fresh, wrong); e.Allowed || e.ReviewsKnown {
		t.Fatal("noncanonical resolver ID accepted", e)
	}
	again, _ := s.Classification(b.ChangeID, 1)
	if !reflect.DeepEqual(stored, again) {
		t.Fatal("decision mutated classification")
	}
	// An unknown caused solely by unresolved membership does not retain the old
	// owner-known flags/status after a fresh successful read.
	s, b, current, policy, fresh = decisionSetup(t, func(classification.Owner) cp.IdentitySet { return cp.IdentitySet{Reason: "membership unavailable"} })
	stored, _ = s.Classification(b.ChangeID, 1)
	if stored.Evidence.Classification.CodeOwnersKnown || stored.Evidence.Classification.Facts["unknown"] != cp.True {
		t.Fatal("missing unknown classification setup")
	}
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)); !e.Allowed || !e.ReviewsKnown {
		t.Fatal("classification-time membership unknown stranded decision", e)
	}
	storedAfter, _ := s.Classification(b.ChangeID, 1)
	if !reflect.DeepEqual(stored, storedAfter) {
		t.Fatal("fresh resolution changed durable facets/generation")
	}
}

func TestDecisionExclusionsPermissionsUnknownAndConditionSet(t *testing.T) {
	s, b, current, policy, fresh := decisionSetup(t, ownerPool(2))
	// Evidence may be retained but only the supplied fresh grants can authorize.
	s = Apply(s, fresh)
	revoked := detached(fresh)
	a := revoked.Actors[person(2).ID]
	a.Grant.Write = cp.False
	revoked.Actors[person(2).ID] = a
	if e := s.evaluateApprovalsTest(policy, current, revoked, ownerPool(2)); e.Allowed || len(e.ReviewLogins) > 0 {
		t.Fatal("revoked writer counted", e)
	}
	for _, who := range []string{"author", "submitter", "accountable"} {
		test := s
		account := Accountability(b, person(7), person(1), nil)
		switch who {
		case "author":
			account = Accountability(b, person(7), person(2), nil)
		case "submitter":
			account.SubmitterID = person(2).ID
		case "accountable":
			account = Accountability(b, person(7), person(1), map[string][]cp.Identity{person(1).ID: {person(2)}})
		}
		test = Apply(test, account)
		if e := test.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)); e.Allowed || len(e.ReviewLogins) > 0 {
			t.Fatal(who, "approval counted", e)
		}
	}
	unknown := detached(fresh)
	unknown.Known = false
	unknown.Reasons = []string{"team unresolved"}
	if e := s.evaluateApprovalsTest(policy, current, unknown, ownerPool(2)); e.Allowed || e.ReviewsKnown {
		t.Fatal("unknown eligibility became empty known list")
	}
	// Alias changes share one stable ID and produce one condition reviewer.
	alias := native(b, 11, cp.Approve, b.At.Add(time.Minute))
	alias.Reviewer.Login = "renamed"
	s = Apply(s, alias)
	e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2))
	if !e.Allowed || len(e.Requirements[0].ApprovedIDs) != 1 || !reflect.DeepEqual(e.ReviewLogins, []string{"reviewer"}) {
		t.Fatal("ID dedupe/condition set", e)
	}
	block := native(b, 12, cp.RequestChanges, b.At.Add(2*time.Minute))
	s = Apply(s, block)
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)); e.Allowed || !reflect.DeepEqual(e.BlockingIDs, []string{person(2).ID}) {
		t.Fatal("eligible changes request did not block", e)
	}
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(9)); len(e.BlockingIDs) > 0 {
		t.Fatal("ineligible changes request blocked")
	}
	// No applicable requirement means no implicit writer pool, including receipt conditions.
	policy.Approvals.Rules = []cp.Rule{{When: cp.Match{Touches: []string{"docs/**"}}, Require: cp.Requirement{Selector: cp.None}}}
	if e := s.evaluateApprovalsTest(policy, current, fresh, nil); !e.Allowed || !e.ReviewsKnown || len(e.ReviewLogins) != 0 {
		t.Fatal("implicit pool", e)
	}
}

func TestReceiptOrderIntentEditDoesNotReviveNative(t *testing.T) {
	// PR 7a supplies acceptance/wire records; these shared Stance values pin the
	// fold/evaluator contract now, without adding a receipt command producer.
	s, b, current, policy, fresh := decisionSetup(t, ownerPool(2))
	policy.Approvals.Rules[0].Require.Receipt = true
	receipt := command(b, 20, cp.Approve, b.At.Add(time.Minute))
	*receipt.Stance.Receipt = current
	s = Apply(s, receipt)
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)); !e.Allowed {
		t.Fatal("current receipt failed", e)
	}
	intent := Intent(b, "edited", "body")
	s = Apply(s, intent)
	current.IntentDigest = intent.Digest
	current.BriefDigest = "new brief"
	if e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2)); e.Allowed || len(e.ReviewLogins) > 0 {
		t.Fatal("intent edit revived native", e)
	}
	s = restart(t, s)
	nativeAfter := native(b, 30, cp.Approve, b.At.Add(2*time.Minute))
	s = Apply(s, nativeAfter)
	e := s.evaluateApprovalsTest(policy, current, fresh, ownerPool(2))
	if e.Allowed || len(e.ReviewLogins) != 1 {
		t.Fatal("native not shared with receipt-only conditions", e)
	}
	v, _ := latest(s, b)
	if v.Event.Stance.Receipt != nil {
		t.Fatal("native bound to observation-time intent")
	}
}

func TestApprovalImmutableApplySnapshotReplayAndInertPlanner(t *testing.T) {
	cfg, legacy, now := chainState(t, 4, 1)
	if legacy.approvalFacts != nil {
		t.Fatal("legacy queue allocated approval facts")
	}
	b := approvalBasis()
	s := approvalState(b)
	events := []Event{Accountability(b, person(7), person(1), nil), Intent(b, "title", "body"), native(b, 10, cp.Approve, b.At), eligibilityEvent(b), command(b, 20, cp.Withdraw, b.At.Add(time.Minute))}
	for i, event := range events {
		prior := detached(s.approvalFacts)
		old := s
		fingerprint := s.ApprovalFingerprint(b.ChangeID)
		s = Apply(s, event)
		if !reflect.DeepEqual(old.approvalFacts, prior) {
			t.Fatal("Apply mutated prior state")
		}
		if s.ApprovalFingerprint(b.ChangeID) == fingerprint {
			t.Fatal("facts did not change fingerprint")
		}
		tail := restart(t, s)
		full := s
		for _, e := range events[i+1:] {
			tail = Apply(tail, e)
			full = Apply(full, e)
		}
		if !reflect.DeepEqual(tail.approvalFacts, full.approvalFacts) || !reflect.DeepEqual(Plan(cfg, tail, now), Plan(cfg, full, now)) {
			t.Fatal("snapshot-plus-tail diverged")
		}
	}
	// Facts do not change planner revisions, timers, classifications or actions.
	bstate := approvalState(b)
	before := Plan(cfg, bstate, now)
	oldRevision := bstate.revision
	for _, e := range events {
		bstate = Apply(bstate, e)
	}
	if bstate.revision != oldRevision || !reflect.DeepEqual(before, Plan(cfg, bstate, now)) || bstate.classifications != nil {
		t.Fatal("facts affected planner or classification")
	}
	facts, _ := bstate.ApprovalFacts(b.ChangeID)
	facts.Sources["tamper"] = AttributedStance{}
	if _, exists := bstate.approvalFacts[b.ChangeID].Sources["tamper"]; exists {
		t.Fatal("read leaked mutable state")
	}
	for _, version := range []int{2, 3, 4} {
		raw, _ := EncodeSnapshot(State{})
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		fields["Version"] = json.RawMessage(fmt.Sprint(version))
		delete(fields, "ApprovalFacts")
		if version < 4 {
			delete(fields, "Classifications")
		}
		if version < 3 {
			delete(fields, "ChangeRevisions")
			delete(fields, "BatchRevisions")
		}
		raw, _ = json.Marshal(fields)
		restored, err := DecodeSnapshot(raw)
		if err != nil || restored.approvalFacts != nil {
			t.Fatal("legacy snapshot", version, err)
		}
	}
}

func TestAccountabilityMappingIntentNormalizationAndForkIsolation(t *testing.T) {
	b := approvalBasis()
	for _, forgeKind := range []cp.AccountKind{cp.HumanAccount, cp.AgentAccount} {
		author := person(1)
		author.Kind = forgeKind
		e := Accountability(b, person(7), author, map[string][]cp.Identity{author.ID: {person(3), person(4)}})
		if e.Validate() != nil || e.Author.Kind != cp.AgentAccount || !e.Recorded || len(e.Humans) != 2 || e.Actor.ID == e.Author.ID {
			t.Fatal("mapping precedence or identity conflation", e)
		}
	}
	bot := person(9)
	bot.Kind = cp.AgentAccount
	e := Accountability(b, person(7), bot, nil)
	if e.Recorded || len(e.Humans) > 0 || e.Reason != "accountable human not recorded" {
		t.Fatal("inferred human for unmapped bot")
	}
	intent := Intent(b, " \r\nTitle\rline\r\n ", " Body\r\ntext ")
	if intent.Title != "Title\nline" || intent.Body != "Body\ntext" || intent.Validate() != nil {
		t.Fatal("normalization")
	}
	changed := intent
	changed.Body += "edit"
	if changed.Validate() == nil {
		t.Fatal("unverified intent digest")
	}
	s := approvalState(b)
	prior := s.observations
	s = Apply(s, Accountability(b, person(7), person(1), nil))
	if !reflect.DeepEqual(prior, s.observations) || len(s.Entries) > 0 {
		t.Fatal("fork accountability granted trust/admission")
	}
}

func TestRecoveryRequiresReobservationAndNeverRelaxesBarrier(t *testing.T) {
	b := approvalBasis()
	block := dismissal(b, 10, forge.ObservationTimeFallback, b.At)
	s := Apply(approvalState(b), block)
	newRead := b
	newRead.ReadID = "second-read"
	positive := native(newRead, 20, cp.Approve, b.At.Add(time.Minute))
	s = Apply(s, positive)
	assertKind(t, s, b, cp.Withdraw)
	// A distinct older/missing blocker cannot lower an established barrier.
	unbounded := command(newRead, 30, cp.Withdraw, b.At)
	unbounded.Stance.Source.TimeKnown = false
	unbounded.Stance.Source.Precision = ""
	unbounded.Stance.Source.UpperTimeBound = time.Time{}
	unbounded.SourceTime.UpperBound = time.Time{}
	s = Apply(restart(t, s), unbounded)
	block.Basis = newRead
	s = Apply(s, block)
	fresh := command(newRead, 40, cp.Approve, b.At.Add(time.Hour))
	s = Apply(s, fresh)
	assertKind(t, s, b, cp.Withdraw)
	// Recovery with no bound is a new revision and a fresh attributed review.
	next := newRead
	next.Revision = 2
	next.Head = strings.Repeat("d", 40)
	next.At = b.At.Add(2 * time.Hour)
	beforeFingerprint := s.ApprovalFingerprint(b.ChangeID)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: next.At}})
	if beforeFingerprint == s.ApprovalFingerprint(b.ChangeID) {
		t.Fatal("revision did not change approval fingerprint")
	}
	s = Apply(restart(t, s), native(next, 50, cp.Approve, next.At.Add(time.Minute)))
	v, _ := latest(s, next)
	if v.Revision != 2 || v.Event.Stance.Kind != cp.Approve {
		t.Fatal("new revision recovery failed")
	}
	// A dismissal discovered later remains attributed to its original review.
	late := dismissal(next, 10, forge.ReviewDismissalTime, next.At.Add(2*time.Minute))
	late.Stance.Head = b.Head
	s = Apply(s, late)
	v, _ = latest(s, next)
	if v.Revision != 2 {
		t.Fatal("historical dismissal displaced current review")
	}
}

func TestReceiptClearsPinnedFallbackOnlyStrictlyAfterBound(t *testing.T) {
	b := approvalBasis()
	blocker := dismissal(b, 10, forge.ObservationTimeFallback, b.At)
	for _, after := range []bool{false, true} {
		s := Apply(approvalState(b), blocker)
		s = restart(t, s)
		read := b
		read.ReadID = "fresh-read"
		poll := blocker
		poll.Basis = read
		s = Apply(s, poll)
		at := blocker.Stance.Source.UpperTimeBound
		if after {
			at = at.Add(time.Second)
		}
		s = Apply(s, command(read, 20, cp.Approve, at))
		want := cp.Withdraw
		if after {
			want = cp.Approve
		}
		assertKind(t, s, b, want)
	}
}

func TestApprovalSnapshotRejectsCorruptBindingsAndMissingState(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 10, cp.Approve, b.At))
	for _, mutate := range []func(*ApprovalFacts){
		func(f *ApprovalFacts) { f.Latest[person(2).ID] = "missing" },
		func(f *ApprovalFacts) { f.Blockers[person(2).ID] = f.Latest[person(2).ID] },
		func(f *ApprovalFacts) { f.Frontiers[person(2).ID] = f.Latest[person(2).ID] },
		func(f *ApprovalFacts) {
			for k, v := range f.Sources {
				v.Revision = 99
				f.Sources[k] = v
			}
		},
		func(f *ApprovalFacts) {
			for k, v := range f.Sources {
				v.Event.Stance.UserID = person(9).ID
				f.Sources[k] = v
			}
		},
	} {
		raw, _ := EncodeSnapshot(s)
		var snapshot stateSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatal(err)
		}
		facts := snapshot.ApprovalFacts[b.ChangeID]
		mutate(&facts)
		snapshot.ApprovalFacts[b.ChangeID] = facts
		bad, _ := json.Marshal(snapshot)
		if _, err := DecodeSnapshot(bad); err == nil {
			t.Fatal("corrupt attribution/index accepted")
		}
	}
	raw, _ := EncodeSnapshot(s)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	delete(fields, "ApprovalFacts")
	raw, _ = json.Marshal(fields)
	if _, err := DecodeSnapshot(raw); err == nil {
		t.Fatal("v5 missing projection accepted")
	}
}

func TestWithinChannelSourceOrderWinsClockDisagreement(t *testing.T) {
	b := approvalBasis()
	// A higher native review ID is newer in its channel. An older-looking
	// timestamp cannot make its changes request disappear behind an approval.
	s := Apply(approvalState(b), native(b, 10, cp.Approve, b.At.Add(time.Minute)))
	s = Apply(s, native(b, 20, cp.RequestChanges, b.At))
	assertKind(t, restart(t, s), b, cp.RequestChanges)
	// Positive source ordering also follows IDs when there is no blocker;
	// blockers still require strict positive time proof in addition to IDs.
	s = Apply(approvalState(b), native(b, 10, cp.Approve, b.At.Add(time.Minute)))
	s = Apply(s, native(b, 20, cp.Approve, b.At))
	v, _ := latest(s, b)
	if v.Event.Stance.Source.ID != "20" {
		t.Fatal("source order replaced by wall time")
	}
}
func TestFreshGroupMembershipReplacesRecordedPool(t *testing.T) {
	s, _, current, policy, fresh := decisionSetup(t, ownerPool(2))
	policy.Approvals.Rules[0].Require = cp.Requirement{Selector: cp.Group, Group: "platform", Count: 1}
	fresh.Groups["platform"] = cp.IdentitySet{IDs: []string{person(2).ID}, Resolved: true, Fresh: true}
	s = Apply(s, fresh)
	if e := s.evaluateApprovalsTest(policy, current, fresh, nil); !e.Allowed {
		t.Fatal("fresh team member not counted", e)
	}
	removed := detached(fresh)
	removed.Groups["platform"] = cp.IdentitySet{IDs: []string{}, Resolved: true, Fresh: true}
	if e := s.evaluateApprovalsTest(policy, current, removed, nil); e.Allowed || len(e.ReviewLogins) > 0 || !e.ReviewsKnown {
		t.Fatal("removed team member survived in stored pool", e)
	}
	unknown := detached(fresh)
	unknown.Groups["platform"] = cp.IdentitySet{IDs: []string{}, Reason: "team membership unavailable"}
	if e := s.evaluateApprovalsTest(policy, current, unknown, nil); e.Allowed || e.ReviewsKnown {
		t.Fatal("unknown membership became known empty", e)
	}
}
func TestLateDismissalJoinsOriginalRevision(t *testing.T) {
	b := approvalBasis()
	s := Apply(approvalState(b), native(b, 10, cp.Approve, b.At))
	next := b
	next.Revision = 2
	next.Head = strings.Repeat("d", 40)
	next.At = b.At.Add(time.Hour)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 2, Predecessor: 1, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: next.At}})
	next.Revision = 3
	next.Head = b.Head
	next.At = b.At.Add(2 * time.Hour)
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 3, Predecessor: 2, HeadSHA: next.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: next.At}})
	s = Apply(restart(t, s), native(next, 30, cp.Approve, next.At.Add(time.Minute)))
	late := dismissal(next, 10, forge.ReviewDismissalTime, next.At.Add(2*time.Minute))
	s = Apply(s, late)
	f, _ := s.ApprovalFacts(b.ChangeID)
	if f.Sources[sourceKey(late)].Revision != 1 {
		t.Fatal("dismissal rebound original review")
	}
	v, _ := latest(restart(t, s), next)
	if v.Revision != 3 || v.Event.Stance.Kind != cp.Approve {
		t.Fatal("historical dismissal displaced current approval")
	}
}

// Acquire the supplied pure resolver result into a fresh wire observation. The
// production boundary consumes this recorded input, not an ephemeral resolver.
func (s State) evaluateApprovalsTest(p cp.Policy, current cp.ReceiptIdentity, fresh ApprovalEligibilityObserved, resolve classification.OwnerResolver) cp.Evaluation {
	fresh = detached(fresh)
	fresh.OwnerPools = map[string]cp.IdentitySet{}
	if stored, ok := s.Classification(ChangeID(current.ChangeID), ChangeRevision(current.Revision)); ok {
		classification.ResolveOwnerEligibility(stored.Evidence, func(owner classification.Owner) cp.IdentitySet {
			pool := cp.IdentitySet{IDs: []string{}, Reason: "fresh owner resolution unavailable"}
			if resolve != nil {
				pool = resolve(owner)
				if pool.IDs == nil {
					pool.IDs = []string{}
				}
			}
			fresh.OwnerPools[owner.Label()] = pool
			return pool
		})
	}
	return s.EvaluateApprovals(p, current, fresh)
}

func TestOwnerMembershipObservationChangesFingerprint(t *testing.T) {
	s, b, current, policy, fresh := decisionSetup(t, ownerPool(2))
	fresh.OwnerPools["owned:user:owner"] = cp.IdentitySet{IDs: []string{person(2).ID}, Fresh: true, Resolved: true}
	s = Apply(s, fresh)
	if e := s.EvaluateApprovals(policy, current, fresh); !e.Allowed {
		t.Fatal(e)
	}
	prior := s.ApprovalFingerprint(b.ChangeID)
	removed := detached(fresh)
	removed.OwnerPools["owned:user:owner"] = cp.IdentitySet{IDs: []string{person(9).ID}, Fresh: true, Resolved: true}
	s = Apply(s, removed)
	if s.ApprovalFingerprint(b.ChangeID) == prior {
		t.Fatal("owner membership invisible to approval-input fingerprint")
	}
	if e := s.EvaluateApprovals(policy, current, removed); e.Allowed {
		t.Fatal("removed owner counted", e)
	}
	// Replay retains exact observed owner pools, but a decision still needs a
	// fresh explicit observation and cannot use the stored snapshot by default.
	s = restart(t, s)
	if e := s.EvaluateApprovals(policy, current, fresh); !e.Allowed {
		t.Fatal("fresh owner recovery failed", e)
	}
	absent := detached(fresh)
	absent.OwnerPools = map[string]cp.IdentitySet{}
	if e := s.EvaluateApprovals(policy, current, absent); e.Allowed || e.ReviewsKnown {
		t.Fatal("stored owner pools leaked into decision", e)
	}
}

func TestLateLowerIDBlockersNeedTimeProofEvenWithinChannel(t *testing.T) {
	b := approvalBasis()
	for _, missing := range []bool{false, true} {
		for _, newer := range []bool{false, true} {
			block := native(b, 10, cp.RequestChanges, b.At)
			if missing {
				block.Stance.Source.TimeKnown = false
				block.Stance.Source.Precision = ""
				block.Stance.Source.Time = time.Time{}
				block.SourceTime.Time = time.Time{}
				block.Stance.Source.UpperTimeBound = time.Time{}
				block.SourceTime.UpperBound = time.Time{}
			}
			at := b.At
			if newer {
				at = b.At.Add(2 * time.Second)
			}
			positive := native(b, 20, cp.Approve, at)
			var folded []State
			for _, events := range [][]Event{{block, positive}, {positive, block}} {
				s := approvalState(b)
				for _, e := range events {
					s = Apply(s, e)
				}
				s = restart(t, s)
				expected := cp.RequestChanges
				if newer && !missing {
					expected = cp.Approve
				}
				assertKind(t, s, b, expected)
				folded = append(folded, s)
			}
			if !reflect.DeepEqual(folded[0].approvalFacts, folded[1].approvalFacts) {
				t.Fatal("same-channel permutation changed fold")
			}
		}
	}
}
