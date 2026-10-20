package serve

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStageAEligibilityJournalDedupe(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", false)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	facts, _ := state.ApprovalFacts(r.ChangeID)
	deny := *facts.Eligibility
	deny.Basis = approvalBasis(r)
	deny.Groups = map[string]cp.IdentitySet{"reviewers": {Resolved: false, IDs: []string{}, Reason: "unavailable"}}
	record := func(e queue.ApprovalEligibilityObserved) {
		t.Helper()
		must(t, s.recordStageA(t.Context(), w, "stage-a/eligibility/"+string(r.ChangeID), e, e))
	}
	record(deny)
	before, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	polls := 20
	if testing.Short() {
		polls = 3
	}
	for i := range polls {
		deny.Basis = approvalBasis(r)
		deny.Reasons = []string{fmt.Sprintf("diagnostic %d", i)}
		deny.Groups["reviewers"] = cp.IdentitySet{Resolved: false, IDs: []string{}, Reason: fmt.Sprintf("transport error %d", i)}
		record(deny)
	}
	after, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	if after != before {
		t.Fatalf("diagnostic-only polling appended events: %d -> %d", before, after)
	}
	// Another worker's gate appends outside the observations side key.
	allow := deny
	allow.Groups = map[string]cp.IdentitySet{"reviewers": {Resolved: true, Fresh: true, IDs: []string{"github.com:user:21"}}}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{allow}, "")
	must(t, err)
	before, err = s.store.Watermark(t.Context(), w.id)
	must(t, err)
	record(deny)
	records, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	if len(records) != 1 {
		t.Fatalf("deny after competing allow suppressed: %d events", len(records))
	}
	current, _ := shadowState(t, s, w).ApprovalFacts(r.ChangeID)
	if current.Eligibility.Groups["reviewers"].Resolved {
		t.Fatal("competing allow survived fresh denial")
	}
}

func TestStageAApprovalReadUsesDatabaseOrder(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "7", false)
	state := shadowState(t, s, w)
	r, _ := state.PRRevision(1)
	facts, _ := state.ApprovalFacts(r.ChangeID)
	records, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	relevant := map[string]queue.Event{}
	for _, record := range records {
		switch record.Event.(type) {
		case queue.ConfigApplied, queue.ChangeRevisionObserved, queue.ChangeClassified,
			queue.ChangeAccountabilityObserved, queue.ChangeIntentObserved, queue.ApprovalEligibilityObserved:
			name, _, err := queue.EncodeEvent(record.Event)
			must(t, err)
			relevant[name] = record.Event
		}
	}
	// Use actual wire fixtures to pin the basis.change_id JSON path for these
	// additive event kinds, even when the projection ignores their semantics.
	for _, name := range []string{"review_stance_observed", "approval_receipt_recorded", "approval_inputs_changed"} {
		raw, err := os.ReadFile("../queue/testdata/wire/v1/event_" + name + ".json")
		must(t, err)
		event, err := queue.DecodeEvent(raw)
		must(t, err)
		basis := facts.Eligibility.Basis
		switch e := event.(type) {
		case queue.ReviewStanceObserved:
			e.Basis = basis
			event = e
		case queue.ApprovalReceiptRecorded:
			e.Basis = basis
			e.Current.ChangeID = string(basis.ChangeID)
			e.Current.Revision = uint64(basis.Revision)
			e.Current.Head = basis.Head
			e.Supplied = e.Current
			event = e
		case queue.ApprovalInputsChanged:
			e.Basis = basis
			e.Eligibility.Basis = basis
			event = e
		}
		relevant[name] = event
	}

	if len(relevant) != 9 {
		t.Fatalf("missing event coverage: %v", relevant)
	}
	for name, event := range relevant {
		t.Run(name, func(t *testing.T) {
			read, err := s.store.BeginApprovalRead(t.Context(), w.id, []queue.ChangeID{r.ChangeID})
			must(t, err)
			// A competing worker commits relevant evidence. Even ABA or skewed
			// clocks cannot make this acquisition skip the committed evidence.
			if config, ok := event.(queue.ConfigApplied); ok {
				config.Digest = strings.Repeat("f", 64)
				event = config
			}
			if revision, ok := event.(queue.ChangeRevisionObserved); ok {
				revision.Predecessor = r.Revision
				revision.Revision = r.Revision + 1
				revision.HeadSHA = strings.Repeat("e", 40)
				revision.Basis.At = time.Now()
				event = revision
			}
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{event}, "")
			must(t, err)
			oldRead := *facts.Eligibility
			oldRead.Basis.At = time.Now().Add(time.Hour)
			oldRead.Basis.ReadID = "skewed-worker"
			err = s.recordStageA(read.Context(t.Context()), w, "skewed", oldRead, oldRead)
			if !errors.Is(err, store.ErrLandingDecisionStale) {
				t.Fatalf("later timestamp bypassed %s commit: %v", name, err)
			}
		})
	}
}

func TestStageAConfigReclassifiesBeforeEligibility(t *testing.T) {
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	before, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	_, err = f.PushBase(repo, "main", "change approvals", map[string]string{".openmerge.yml": "version: 1\nqueues:\n- name: default\n  merge_when: [check-success = ci]\n  approvals: {default: {any_writer: true, count: 2}}\n"})
	must(t, err)
	pr := w.prs[1]
	f.SetNativeReviews(repo, 1, forge.NativeReviews{Reviews: []forge.NativeReview{{ID: 200, CommitID: pr.HeadSHA, Author: forge.UserIdentity{ID: 21, Login: "writer", Kind: forge.UserAccount}, State: forge.ReviewApproved, Submitted: forge.NativeTime{Time: time.Now().UTC().Add(time.Minute), Source: forge.ReviewSubmissionTime, Precision: time.Second}}}})
	must(t, s.reconcile(t.Context(), w, true))
	records, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	configSeen, classified, observed := false, false, false
	for _, record := range records {
		switch e := record.Event.(type) {
		case queue.ConfigApplied:
			configSeen, classified = true, false
		case queue.ChangeClassified:
			if configSeen && e.Evidence.Classification.Inputs.PolicyDigest == w.stageA.policy.Digest {
				classified = true
			}
		case queue.ApprovalEligibilityObserved:
			if !configSeen || !classified {
				t.Fatal("eligibility preceded current-policy classification")
			}
			observed = true
		}
	}
	if !configSeen || !classified || !observed {
		t.Fatalf("missing config/classification/eligibility: %t/%t/%t", configSeen, classified, observed)
	}
}

func TestStageACheckGateRequiresCurrentPolicyBeforeJournaling(t *testing.T) {
	s, w, f := shadowFixture(t, 1, "main", "7", false)
	f.SetNativeReviews(repo, 1, shadowNative(f, w, shadowComment(100, "@openmerge queue")))
	batch := shadowBuildBatch(t, s, w, 1)
	before, err := s.store.Watermark(t.Context(), w.id)
	must(t, err)
	w.stageA.policy.Digest = strings.Repeat("e", 64)
	if _, _, err := s.stageACheckPolicy(t.Context(), w, batch, nil); err == nil {
		t.Fatal("old classification accepted under changed approvals")
	}
	records, err := s.store.EventsAfter(t.Context(), w.id, before)
	must(t, err)
	for _, record := range records {
		switch record.Event.(type) {
		case queue.ApprovalEligibilityObserved, queue.ApprovalGateRejected:
			t.Fatalf("gate journaled %T before current-policy evidence", record.Event)
		}
	}
}
