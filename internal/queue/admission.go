package queue

import (
	"errors"
	"fmt"
)

const maxClassificationChangeBytes = 16 << 20

// SnapshotBudgetError identifies a full-snapshot admission exceeding the decoder
// bound, even when the shared evidence reserve itself still fits.
type SnapshotBudgetError struct {
	ChangeID      ChangeID
	SnapshotBytes int
}

func (e SnapshotBudgetError) Error() string {
	return fmt.Sprintf("snapshot admission rejected for %s: %d bytes", e.ChangeID, e.SnapshotBytes)
}

// checkSharedAdmissionBudget applies the per-approval cap and the shared
// evidence reserve. admitted identifies the event being considered, so a
// queue-level failure names the admission that must be rolled back.
func checkSharedAdmissionBudget(s State, admitted ChangeID) error {
	_, err := EncodeSnapshot(s)
	return remapAdmissionBudgetError(err, admitted)
}

// remapAdmissionBudgetError attaches the ChangeID whose event was being
// admitted to either typed snapshot-budget error. Snapshot validation may
// discover a queue-level overflow without knowing which new event caused it;
// non-budget errors are returned unchanged.
func remapAdmissionBudgetError(err error, admitted ChangeID) error {
	if err == nil || admitted == "" {
		return err
	}
	var classification ClassificationSnapshotBudgetError
	if errors.As(err, &classification) {
		classification.ChangeID = admitted
		return classification
	}
	var approval ApprovalSnapshotBudgetError
	if errors.As(err, &approval) {
		approval.ChangeID = admitted
		return approval
	}
	var snapshot SnapshotBudgetError
	if errors.As(err, &snapshot) {
		snapshot.ChangeID = admitted
		return snapshot
	}
	return err
}

// CheckApprovalAdmission preflights any approval projection event without
// mutating s. Producers should call it before journaling the event.
func (s State) CheckApprovalAdmission(e Event) error {
	basis, err := approvalAdmissionBasis(e)
	if err != nil {
		return err
	}
	if err := s.CheckApprovalBoundary(basis); err != nil {
		return err
	}

	if receipt, ok := e.(ApprovalReceiptRecorded); ok {
		return s.CheckReceiptAdmission(receipt)
	}
	next := Apply(s, e)
	return checkSharedAdmissionBudget(next, basis.ChangeID)
}

// CheckStanceAdmission is the stance-specific name used by review producers.
// It includes the approval projection and the shared classification/receipt
// reserve because a stance can create a new approval fact.
func (s State) CheckStanceAdmission(e ReviewStanceObserved) error {
	return s.CheckApprovalAdmission(e)
}

func approvalAdmissionBasis(e Event) (ApprovalBasis, error) {
	switch e := e.(type) {
	case ChangeAccountabilityObserved:
		return e.Basis, e.Validate()
	case ChangeIntentObserved:
		return e.Basis, e.Validate()
	case ApprovalEligibilityObserved:
		return e.Basis, e.Validate()
	case ReviewStanceObserved:
		return e.Basis, e.Validate()
	case ApprovalReceiptRecorded:
		return e.Basis, e.Validate()
	default:
		return ApprovalBasis{}, fmt.Errorf("unsupported approval admission event %T", e)
	}
}
