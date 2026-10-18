package queue

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

func admissionClassification(t *testing.T, pr, reasonBytes int) ChangeClassified {
	t.Helper()
	e := classificationEvent(t)
	id, err := PRChangeID("7", "Owner/Repo", "release", "", pr)
	if err != nil {
		t.Fatal(err)
	}
	e.Evidence.Classification.Inputs.ChangeID = string(id)
	e.Evidence.Classification.CodeOwners = []cp.CodeOwner{{
		Path:     "docs/a.md",
		Side:     cp.NewSide,
		Eligible: cp.IdentitySet{Reason: strings.Repeat("x", reasonBytes)},
	}}
	return e
}

func TestSharedAdmissionReserveAllowsThreeLargeAndSmaller(t *testing.T) {
	s := State{}
	for pr := 1; pr <= 3; pr++ {
		e := admissionClassification(t, pr, 14<<20)
		if err := s.CheckClassificationAdmission(e); err != nil {
			t.Fatalf("large classification %d rejected: %v", pr, err)
		}
		s = Apply(s, e)
	}

	b := approvalBasis()
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: b.At.Add(-1)}})
	small := eligibilityEvent(b)
	small.Reasons = []string{strings.Repeat("s", 5<<20)}
	if err := s.CheckApprovalAdmission(small); err != nil {
		t.Fatalf("smaller approval evidence rejected: %v", err)
	}
	s = Apply(s, small)
	f, ok := s.ApprovalFacts(b.ChangeID)
	if !ok || len(f.Eligibility.Reasons[0]) != 5<<20 {
		t.Fatal("smaller approval evidence was not admitted")
	}

	classification := admissionClassification(t, 4, 12<<20)
	var classificationBudget ClassificationSnapshotBudgetError
	if err := s.CheckClassificationAdmission(classification); !errors.As(err, &classificationBudget) || classificationBudget.ChangeID != ChangeID(classification.Evidence.Classification.Inputs.ChangeID) {
		t.Fatal("classification budget did not name the admitted change", err)
	}
	if _, ok := s.Classification(ChangeID(classification.Evidence.Classification.Inputs.ChangeID), 1); ok {
		t.Fatal("rejected classification changed state")
	}

	large := eligibilityEvent(b)
	large.Reasons = []string{strings.Repeat("l", 7<<20)}
	if err := s.CheckApprovalAdmission(large); !errors.As(err, &classificationBudget) || classificationBudget.ChangeID != b.ChangeID {
		t.Fatal("shared approval budget did not name the admitted change", err)
	}
	f, _ = s.ApprovalFacts(b.ChangeID)
	if len(f.Eligibility.Reasons[0]) != 5<<20 {
		t.Fatal("rejected approval admission changed state")
	}
}

func TestStanceAdmissionUsesSharedReserve(t *testing.T) {
	s := State{}
	for pr := 1; pr <= 4; pr++ {
		s = Apply(s, admissionClassification(t, pr, 12<<20))
	}
	b := approvalBasis()
	s = Apply(s, ChangeRevisionObserved{PR: 12, ChangeID: b.ChangeID, Revision: 1, HeadSHA: b.Head, Basis: RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: b.At.Add(-1)}})

	var budget ClassificationSnapshotBudgetError
	if err := s.CheckStanceAdmission(native(b, 10, cp.Approve, b.At)); !errors.As(err, &budget) || budget.ChangeID != b.ChangeID {
		t.Fatal("stance budget did not name the admitted change", err)
	}
	if _, ok := s.ApprovalFacts(b.ChangeID); ok {
		t.Fatal("rejected stance admission changed state")
	}
}

func TestRemapAdmissionBudgetError(t *testing.T) {
	id, err := PRChangeID("7", "Owner/Repo", "release", "", 99)
	if err != nil {
		t.Fatal(err)
	}
	old, err := PRChangeID("7", "Owner/Repo", "release", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var classification ClassificationSnapshotBudgetError
	if !errors.As(remapAdmissionBudgetError(ClassificationSnapshotBudgetError{ChangeID: old}, id), &classification) || classification.ChangeID != id {
		t.Fatal("classification budget error was not remapped", classification)
	}
	var approval ApprovalSnapshotBudgetError
	if !errors.As(remapAdmissionBudgetError(ApprovalSnapshotBudgetError{ChangeID: old}, id), &approval) || approval.ChangeID != id {
		t.Fatal("approval budget error was not remapped", approval)
	}
	plain := errors.New("not a budget error")
	if remapAdmissionBudgetError(plain, id) != plain {
		t.Fatal("non-budget error was changed")
	}
}

func TestReceiptAdmissionNamesAdmittedChange(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	receipt, _ := realReceipt(t, s, b, p, "99", b.At)
	for pr := 1; pr <= 3; pr++ {
		s = Apply(s, admissionClassification(t, pr, 15<<20))
	}
	// Leave one byte in the shared reserve so a small receipt crosses the
	// budget after the earlier admitted classifications in sorted order.
	used := 0
	for _, records := range s.classifications {
		raw, _ := json.Marshal(records)
		used += len(raw)
	}
	for _, facts := range s.approvalFacts {
		raw, _ := json.Marshal(facts)
		used += len(raw)
	}
	fresh := eligibilityEvent(b)
	base, _ := json.Marshal(fresh)
	fresh.Reasons = []string{strings.Repeat("x", maxApprovalQueueBytes-used-len(base)-10000)}
	s = Apply(s, fresh)
	// Fill the exact remaining reserve in a recorded diagnostic.
	facts := s.approvalFacts[b.ChangeID]
	used = 0
	for _, records := range s.classifications {
		raw, _ := json.Marshal(records)
		used += len(raw)
	}
	for _, f := range s.approvalFacts {
		raw, _ := json.Marshal(f)
		used += len(raw)
	}
	facts.Eligibility.Reasons[0] += strings.Repeat("x", maxApprovalQueueBytes-used-1)
	s.approvalFacts[b.ChangeID] = facts
	var budget ClassificationSnapshotBudgetError
	if err := s.CheckReceiptAdmission(receipt); !errors.As(err, &budget) || budget.ChangeID != b.ChangeID {
		t.Fatal("receipt budget did not name admitted change", err)
	}
}

func TestAdmissionChecksFullSnapshotBound(t *testing.T) {
	s, b, _, p, _ := decisionSetup(t, ownerPool(2))
	receipt, _ := realReceipt(t, s, b, p, "99", b.At)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	s.pause.Reason = strings.Repeat("x", maxSnapshotBytes-len(raw)-8)
	if _, err = EncodeSnapshot(s); err != nil {
		t.Fatal("prior snapshot must fit", err)
	}
	var budget SnapshotBudgetError
	if err = s.CheckReceiptAdmission(receipt); !errors.As(err, &budget) || budget.ChangeID != b.ChangeID {
		t.Fatal("full snapshot limit not preflighted", err)
	}
}
