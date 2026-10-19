package serve

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestStageAEligibilityJournalDedupe(t *testing.T) {
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
	for i := range 20 {
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
