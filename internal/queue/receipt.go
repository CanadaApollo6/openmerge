package queue

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	BoundaryAt time.Time          `json:"boundary_at"`
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
// permission; identity is built from State. No command is wired here.
func (s State) PrepareReceipt(policy cp.Policy, b ApprovalBasis, actor cp.Identity, commentID string, created time.Time, supplied cp.ReceiptIdentity) (ApprovalReceiptRecorded, error) {
	current, err := s.RecordedBrief(policy, b)
	if err != nil {
		return ApprovalReceiptRecorded{}, err
	}
	identity, err := brief.Identity(current)
	if err != nil {
		return ApprovalReceiptRecorded{}, err
	}
	return s.prepareReceiptIdentity(b, actor, commentID, created, supplied, identity)
}

func (s State) prepareReceiptIdentity(b ApprovalBasis, actor cp.Identity, commentID string, created time.Time, supplied, identity cp.ReceiptIdentity) (ApprovalReceiptRecorded, error) {
	boundary, ok := s.approvalBinding(b)
	if !ok {
		return ApprovalReceiptRecorded{}, ApprovalBoundaryError{b}
	}
	e := ApprovalReceiptRecorded{BoundaryAt: boundary.Basis.At, Basis: b, Supplied: supplied, Current: identity, Actor: actor, CommentID: commentID, CreatedAt: created}
	e.ReasonCode = e.rejectionReason()
	e.Accepted = e.ReasonCode == ""
	return e, e.Validate()
}

// Validate checks explicit bindings and the recorded acceptance outcome.
func (e ApprovalReceiptRecorded) Validate() error {
	if e.Basis.Validate() != nil || !validIdentity(e.Actor) || !validSourceID(e.CommentID) || e.CreatedAt.IsZero() || e.BoundaryAt.IsZero() || !boundedReceiptIdentity(e.Supplied) || !e.Current.Recorded() || !digestHex(e.Current.BriefDigest) || e.Current.ChangeID != string(e.Basis.ChangeID) || e.Current.Revision != uint64(e.Basis.Revision) || e.Current.Head != e.Basis.Head {
		return fmt.Errorf("invalid receipt processing binding")
	}
	reason := e.rejectionReason()
	if e.Accepted != (reason == "") || e.ReasonCode != reason {
		return fmt.Errorf("invalid receipt outcome")
	}

	return nil
}

// ParseReceiptDigest accepts surrounding whitespace, then exactly 64 lowercase hex bytes.
func ParseReceiptDigest(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !digestHex(value) {
		return "", fmt.Errorf("invalid receipt digest")
	}
	return value, nil
}

// Supplied strings are audit data, including rejected bindings, and are bounded
// independently of the acceptance comparison. Empty values remain rejectable evidence.
func boundedReceiptIdentity(r cp.ReceiptIdentity) bool {
	for _, v := range []string{r.ChangeID, r.Base, r.Head, r.MergeBase, r.IntentDigest, r.PolicyDigest, r.BriefDigest} {
		if len(v) > 1024 || !utf8.ValidString(v) {
			return false
		}
	}
	return true
}
func (e ApprovalReceiptRecorded) rejectionReason() string {
	if !digestHex(e.Supplied.BriefDigest) {
		return "receipt_digest_malformed"
	}
	if e.Supplied.BriefDigest != e.Current.BriefDigest {
		return "receipt_digest_mismatch"
	}
	if !e.Supplied.Recorded() || e.Supplied != e.Current {
		return "receipt_binding_mismatch"
	}
	// Same 2s clock uncertainty + 1s resolution used for PR 6 returning heads.
	if !e.CreatedAt.After(e.BoundaryAt.Add(3 * time.Second)) {
		return "receipt_before_revision"
	}
	return ""
}

// PrepareBareReceipt is 11a's bare-digest contract: construct all bindings from
// current RECORDED evidence, retaining the supplied digest verbatim after parsing.
func (s State) PrepareBareReceipt(policy cp.Policy, b ApprovalBasis, actor cp.Identity, commentID string, created time.Time, digest string) (ApprovalReceiptRecorded, error) {
	parsed, parseErr := ParseReceiptDigest(digest)
	if parseErr != nil {
		// Preserve bounded raw audit data, including invalid UTF-8 as replacement
		// characters. Truncate by rune so every rejected event remains encodable.
		parsed = strings.ToValidUTF8(digest, "�")
		if len(parsed) > 1024 {
			parsed = parsed[:1024]
			for !utf8.ValidString(parsed) {
				parsed = parsed[:len(parsed)-1]
			}
		}
	}
	current, err := s.RecordedBrief(policy, b)
	if err != nil {
		return ApprovalReceiptRecorded{}, err
	}
	supplied, err := brief.Identity(current)
	if err != nil {
		return ApprovalReceiptRecorded{}, err
	}
	identity := supplied
	supplied.BriefDigest = parsed
	return s.prepareReceiptIdentity(b, actor, commentID, created, supplied, identity)
}

func (e ApprovalReceiptRecorded) stance() ReviewStanceObserved {
	seq, _ := strconv.ParseUint(e.CommentID, 10, 64)
	return ReviewStanceObserved{Basis: e.Basis, Reviewer: e.Actor, Stance: cp.Stance{UserID: e.Actor.ID, Kind: cp.Approve, Revision: e.Supplied.Revision, Head: e.Supplied.Head, Receipt: &e.Supplied, Source: cp.StanceSource{Channel: cp.CommentCommand, ID: e.CommentID, Sequence: seq, Time: e.CreatedAt, TimeKnown: true, UpperTimeBound: e.CreatedAt.Add(time.Second), Precision: cp.ExactTime, ActingUserID: e.Actor.ID}}, SourceKind: forge.CommentCreationTime, SourceTime: forge.NativeTime{Time: e.CreatedAt, Source: forge.CommentCreationTime, Precision: time.Second, UpperBound: e.CreatedAt.Add(time.Second)}}
}

const maxRejectedReceiptsPerActor = 64

type receiptProjection map[ChangeID]map[string]ApprovalReceiptRecorded

func receiptSourceMatches(e ApprovalReceiptRecorded, source AttributedStance) bool {
	v := source.Event
	return v.Stance.Kind == cp.Approve && v.Stance.Receipt != nil && *v.Stance.Receipt == e.Supplied && v.Stance.UserID == e.Actor.ID && v.Stance.Source.Time.Equal(e.CreatedAt) && v.Basis.ChangeID == e.Basis.ChangeID && v.Basis.Revision == e.Basis.Revision && v.Basis.Head == e.Basis.Head && source.Revision == e.Basis.Revision
}

func (s State) recordReceipt(e ApprovalReceiptRecorded) State {
	boundary, ok := s.approvalBinding(e.Basis)
	if e.Validate() != nil || !ok || !e.BoundaryAt.Equal(boundary.Basis.At) {
		return s
	}
	if _, exists := s.receipts[e.Basis.ChangeID][e.CommentID]; exists {
		return s
	}
	if e.Accepted {
		next, _ := s.observeApproval(e.stance())
		source, ok := next.approvalFacts[e.Basis.ChangeID].Sources[sourceKey(e.stance())]
		if !ok || !receiptSourceMatches(e, source) {
			return s
		}
		s = next
	} else {
		count := 0
		for _, old := range s.receipts[e.Basis.ChangeID] {
			if !old.Accepted && old.Actor.ID == e.Actor.ID {
				count++
			}
		}
		if count >= maxRejectedReceiptsPerActor {
			return s
		}
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
	return s
}

// ReceiptRecords returns detached acceptance and rejection audit evidence.
func (s State) ReceiptRecords(id ChangeID) map[string]ApprovalReceiptRecorded {
	return maps.Clone(s.receipts[id])
}

func validateReceiptSnapshot(p receiptProjection, approvals approvalProjection, revisions map[int]RevisionRecord) error {
	total := 0
	for id, records := range p {
		var revision RevisionRecord
		for _, r := range revisions {
			if r.ChangeID == id {
				revision = r
			}
		}
		rejected := map[string]int{}
		for key, e := range records {
			total++
			if revision.Revision == 0 || e.Basis.Revision > revision.Revision || e.Basis.Revision == revision.Revision && (e.Basis.Head != revision.HeadSHA || !e.BoundaryAt.Equal(revision.Basis.At)) {
				return fmt.Errorf("receipt basis contradicts revision")
			}
			if e.Basis.Revision < revision.Revision {
				old, ok := approvals[id].ReceiptBoundaries[e.Basis.Revision]
				if !ok || old.HeadSHA != e.Basis.Head || !old.Basis.At.Equal(e.BoundaryAt) {
					return fmt.Errorf("receipt basis contradicts old revision")
				}
			}
			if !e.Accepted {
				rejected[e.Actor.ID]++
				if rejected[e.Actor.ID] > maxRejectedReceiptsPerActor {
					return fmt.Errorf("receipt actor rejection budget exceeded")
				}
			}
			if e.Accepted {
				source, ok := approvals[id].Sources[string(cp.CommentCommand)+"/"+key]
				if !ok || !receiptSourceMatches(e, source) {
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
	if _, ok := next.receipts[e.Basis.ChangeID][e.CommentID]; !ok {
		return fmt.Errorf("receipt admission capacity exceeded")
	}
	if err := validateReceiptSnapshot(next.receipts, next.approvalFacts, next.changeRevisions.snapshot()); err != nil {
		return err
	}
	return checkSharedAdmissionBudget(next, e.Basis.ChangeID)
}
