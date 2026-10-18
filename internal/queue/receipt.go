package queue

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ApprovalReceiptRecorded retains both accepted and rejected attempts by comment
// ID. Basis is the current processing revision; Supplied may name an old one.
// Rejections never enter the stance fold. Unapprove remains ReviewStanceObserved.
type ApprovalReceiptRecorded struct {
	Basis      ApprovalBasis      `json:"basis"`
	Supplied   cp.ReceiptIdentity `json:"supplied"`
	Current    cp.ReceiptIdentity `json:"current"`
	Actor      cp.Identity        `json:"actor"`
	CommentID  string             `json:"comment_id"`
	CreatedAt  time.Time          `json:"created_at"`
	Accepted   bool               `json:"accepted"`
	ReasonCode string             `json:"reason_code"`
}

func (ApprovalReceiptRecorded) queueEvent() {}

func digestHex(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == s
}

// PrepareReceipt is pure acceptance validation. The caller authenticates writer
// permission and supplies the current recorded brief; no command is wired here.
func PrepareReceipt(b ApprovalBasis, actor cp.Identity, commentID string, created time.Time, supplied cp.ReceiptIdentity, current brief.Fact) (ApprovalReceiptRecorded, error) {
	identity, err := brief.Identity(current)
	if err != nil {
		return ApprovalReceiptRecorded{}, err
	}
	e := ApprovalReceiptRecorded{Basis: b, Supplied: supplied, Current: identity, Actor: actor, CommentID: commentID, CreatedAt: created}
	e.Accepted = brief.ValidateReceipt(supplied, current) == nil
	if !e.Accepted {
		e.ReasonCode = "receipt_binding_mismatch"
	}
	return e, e.Validate()
}

// Validate checks explicit bindings and the recorded acceptance outcome.
func (e ApprovalReceiptRecorded) Validate() error {
	if e.Basis.Validate() != nil || !validIdentity(e.Actor) || !validSourceID(e.CommentID) || e.CreatedAt.IsZero() || !e.Current.Recorded() || !digestHex(e.Current.BriefDigest) || e.Current.ChangeID != string(e.Basis.ChangeID) || e.Current.Revision != uint64(e.Basis.Revision) || e.Current.Head != e.Basis.Head {
		return fmt.Errorf("invalid receipt processing binding")
	}
	valid := e.Supplied.Recorded() && e.Supplied == e.Current
	if e.Accepted != valid || valid && e.ReasonCode != "" || !valid && e.ReasonCode != "receipt_binding_mismatch" {
		return fmt.Errorf("invalid receipt outcome")
	}
	return nil
}
func (e ApprovalReceiptRecorded) stance() ReviewStanceObserved {
	seq, _ := strconv.ParseUint(e.CommentID, 10, 64)
	return ReviewStanceObserved{Basis: e.Basis, Reviewer: e.Actor, Stance: cp.Stance{UserID: e.Actor.ID, Kind: cp.Approve, Revision: e.Supplied.Revision, Head: e.Supplied.Head, Receipt: &e.Supplied, Source: cp.StanceSource{Channel: cp.CommentCommand, ID: e.CommentID, Sequence: seq, Time: e.CreatedAt, TimeKnown: true, UpperTimeBound: e.CreatedAt.Add(time.Second), Precision: cp.ExactTime, ActingUserID: e.Actor.ID}}, SourceKind: forge.CommentCreationTime, SourceTime: forge.NativeTime{Time: e.CreatedAt, Source: forge.CommentCreationTime, Precision: time.Second, UpperBound: e.CreatedAt.Add(time.Second)}}
}

type receiptProjection map[ChangeID]map[string]ApprovalReceiptRecorded

func (s State) recordReceipt(e ApprovalReceiptRecorded) State {
	if e.Validate() != nil || s.CheckApprovalBoundary(e.Basis) != nil {
		return s
	}
	if _, exists := s.receipts[e.Basis.ChangeID][e.CommentID]; exists {
		return s
	}
	s.receipts = maps.Clone(s.receipts)
	if s.receipts == nil {
		s.receipts = receiptProjection{}
	}
	records := maps.Clone(s.receipts[e.Basis.ChangeID])
	if records == nil {
		records = map[string]ApprovalReceiptRecorded{}
	}
	// Source pins cannot be evicted and rebound. 11a must reject admission on the
	// typed snapshot budget before journaling.
	records[e.CommentID] = e
	s.receipts[e.Basis.ChangeID] = records
	if e.Accepted {
		s, _ = s.observeApproval(e.stance())
	}
	return s
}

// ReceiptRecords returns detached acceptance and rejection audit evidence.
func (s State) ReceiptRecords(id ChangeID) map[string]ApprovalReceiptRecorded {
	return maps.Clone(s.receipts[id])
}

func validateReceiptSnapshot(p receiptProjection, approvals approvalProjection) error {
	total := 0
	for id, records := range p {
		for key, e := range records {
			total++
			if e.Accepted {
				source, ok := approvals[id].Sources[string(cp.CommentCommand)+"/"+key]
				if !ok || source.Event.Stance.Receipt == nil || *source.Event.Stance.Receipt != e.Supplied || source.Event.Stance.UserID != e.Actor.ID {
					return fmt.Errorf("accepted receipt missing stance evidence")
				}
			}
			if total > maxSnapshotEntries || id != e.Basis.ChangeID || key != e.CommentID || e.Validate() != nil {
				return fmt.Errorf("invalid receipt snapshot")
			}
		}
	}
	return nil
}
func upgradeReceiptSnapshot(data []byte, version int) ([]byte, error) {
	data, err := upgradeApprovalSnapshot(data, version)
	if err != nil || version >= 6 {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if _, exists := fields["Receipts"]; exists {
		return nil, fmt.Errorf("old snapshot contains receipts")
	}
	fields["Receipts"] = json.RawMessage(`null`)
	return json.Marshal(fields)
}

// CheckReceiptAdmission validates the durable boundary and recoverable byte
// budgets before 11a journals a receipt (including a rejected binding).
func (s State) CheckReceiptAdmission(e ApprovalReceiptRecorded) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := s.CheckApprovalBoundary(e.Basis); err != nil {
		return err
	}
	next := s.recordReceipt(e)
	if err := validateReceiptSnapshot(next.receipts, next.approvalFacts); err != nil {
		return err
	}
	if err := checkApprovalSnapshotBudget(next.approvalFacts); err != nil {
		return err
	}
	return checkClassificationSnapshotBudget(next.classifications, next.approvalFacts, next.receipts)
}
