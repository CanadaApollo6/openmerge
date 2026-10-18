package queue

import (
	"fmt"
	"reflect"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// LandingSourceRecord freezes an approval's complete acquisition evidence once.
// Obligations reference this table, so a shared approval has one source record.
// Approvals have no dismissal fields; Source retains their explicitly empty
// values too. Receipt retains the original accepted processing record verbatim.
type LandingSourceRecord struct {
	Basis              ApprovalBasis                `json:"basis"`
	Reviewer           wireReceiptActor             `json:"reviewer"`
	Source             landingStanceSource          `json:"source"`
	SourceKind         forge.NativeTimeSource       `json:"source_kind"`
	SourceTime         landingNativeTime            `json:"source_time"`
	Observation        landingNativeObservation     `json:"observation"`
	OriginalSubmission landingNativeTime            `json:"original_submission"`
	Receipt            *wireApprovalReceiptRecorded `json:"receipt"`
}

type landingStanceSource struct {
	Channel           cp.SourceChannel `json:"channel"`
	ID                string           `json:"id"`
	Sequence          uint64           `json:"sequence"`
	Time              time.Time        `json:"time"`
	TimeKnown         bool             `json:"time_known"`
	UpperTimeBound    time.Time        `json:"upper_time_bound"`
	Precision         cp.TimePrecision `json:"precision"`
	DismissedReviewID string           `json:"dismissed_review_id"`
	ActingUserID      string           `json:"acting_user_id"`
}
type landingNativeTime struct {
	Time        time.Time              `json:"time"`
	Source      forge.NativeTimeSource `json:"source"`
	Precision   time.Duration          `json:"precision"`
	Uncertainty time.Duration          `json:"uncertainty"`
	UpperBound  time.Time              `json:"upper_bound"`
}
type landingNativeObservation struct {
	ObservedAt   time.Time `json:"observed_at"`
	ResponseDate time.Time `json:"response_date"`
}

func landingSource(e ReviewStanceObserved, receipt *ApprovalReceiptRecorded) LandingSourceRecord {
	r := LandingSourceRecord{Basis: e.Basis, Reviewer: wireReceiptActor(e.Reviewer), Source: landingStanceSource(e.Stance.Source), SourceKind: e.SourceKind, SourceTime: landingNativeTime(e.SourceTime), Observation: landingNativeObservation(e.Observation), OriginalSubmission: landingNativeTime(e.OriginalSubmission)}
	if receipt != nil {
		w := receiptToWire(*receipt)
		r.Receipt = &w
	}
	return r
}

func (r LandingSourceRecord) event(identity cp.ReceiptIdentity) ReviewStanceObserved {
	e := ReviewStanceObserved{Basis: r.Basis, Reviewer: cp.Identity(r.Reviewer), Stance: cp.Stance{UserID: r.Reviewer.ID, Kind: cp.Approve, Revision: identity.Revision, Head: identity.Head, Source: cp.StanceSource(r.Source)}, SourceKind: r.SourceKind, SourceTime: forge.NativeTime(r.SourceTime), Observation: forge.NativeObservation(r.Observation), OriginalSubmission: forge.NativeTime(r.OriginalSubmission)}
	if r.Receipt != nil {
		record := r.Receipt.event()
		e.Stance.Receipt = &record.Supplied
	}
	return e
}
func (r LandingSourceRecord) key() string   { return string(r.Source.Channel) + "/" + r.Source.ID }
func (r LandingApprovalSource) key() string { return string(r.Channel) + "/" + r.SourceID }

func validateLandingSources(a *LandingApprovalDecision) error {
	if a.Sources == nil {
		return fmt.Errorf("landing sources must be recorded")
	}
	records := map[string]LandingSourceRecord{}
	for _, r := range a.Sources {
		e := r.event(cp.ReceiptIdentity(a.Identity))
		if _, exists := records[r.key()]; exists || e.Validate() != nil || e.Basis.ChangeID != a.Basis.ChangeID || e.Basis.Revision != a.Basis.Revision || e.Basis.Head != a.Basis.Head || r.Source.DismissedReviewID != "" {
			return fmt.Errorf("invalid landing source record")
		}
		if r.Receipt != nil {
			receipt := r.Receipt.event()
			if receipt.Validate() != nil || receipt.CommentID != r.Source.ID || !receipt.Accepted || receipt.Supplied != cp.ReceiptIdentity(a.Identity) || !receiptSourceMatches(receipt, AttributedStance{Event: e, Revision: a.Basis.Revision, LastReadID: e.Basis.ReadID}) {
				return fmt.Errorf("invalid landing receipt record")
			}
		}
		records[r.key()] = r
	}
	used := map[string]bool{}
	for _, obligation := range a.Requirements {
		for _, ref := range obligation.ReliedOn {
			record, exists := records[ref.key()]
			if !exists || record.Reviewer.ID != ref.UserID || record.Basis.ReadID != ref.ReadID || (record.Receipt == nil) != (ref.ReceiptID == "") {
				return fmt.Errorf("landing obligation missing exact source")
			}
			used[ref.key()] = true
		}
	}
	if len(used) != len(records) {
		return fmt.Errorf("extra landing source record")
	}
	return nil
}

func addLandingSource(a *LandingApprovalDecision, e ReviewStanceObserved, receipt *ApprovalReceiptRecorded) error {
	record := landingSource(e, receipt)
	for _, old := range a.Sources {
		if old.key() == record.key() {
			if !reflect.DeepEqual(old, record) {
				return fmt.Errorf("conflicting landing source")
			}
			return nil
		}
	}
	a.Sources = append(a.Sources, record)
	return nil
}
