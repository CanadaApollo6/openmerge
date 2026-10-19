package status

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// DecisionView contains public evidence fields, never a persisted queue DTO.
type DecisionView struct {
	EligibilityEventID int64                     `json:"eligibility_event_id"`
	Accountability     *AccountabilityView       `json:"accountability"`
	EventID            int64                     `json:"event_id"`
	SettlementEventID  int64                     `json:"settlement_event_id"`
	BatchID            string                    `json:"batch_id"`
	AttemptID          string                    `json:"attempt_id"`
	ChangeID           string                    `json:"change_id"`
	Revision           uint64                    `json:"revision"`
	Variant            string                    `json:"variant"`
	Outcome            string                    `json:"outcome"`
	TestedSHA          string                    `json:"tested_sha"`
	IntendedLandingSHA string                    `json:"intended_landing_sha"`
	LandedSHA          string                    `json:"landed_sha"`
	EligibilityReadID  string                    `json:"eligibility_read_id"`
	BriefDigest        string                    `json:"brief_digest"`
	IntentDigest       string                    `json:"intent_digest"`
	PolicyDigest       string                    `json:"policy_digest"`
	Sources            []DecisionSourceView      `json:"sources"`
	Requirements       []DecisionRequirementView `json:"requirements"`
}

// DecisionSourceView references a recorded review or receipt acquisition.
type DecisionSourceView struct {
	UserID       string `json:"user_id"`
	Channel      string `json:"channel"`
	SourceID     string `json:"source_id"`
	ReadID       string `json:"read_id"`
	ReceiptID    string `json:"receipt_id"`
	IntentDigest string `json:"intent_digest"`
	BriefDigest  string `json:"brief_digest"`
}

// DecisionRequirementView retains an expanded immutable landing obligation.
type DecisionRequirementView struct {
	Cause       string               `json:"cause"`
	Path        string               `json:"path"`
	Side        string               `json:"side"`
	Selector    string               `json:"selector"`
	Group       string               `json:"group"`
	Human       bool                 `json:"human"`
	Receipt     bool                 `json:"receipt"`
	Count       int                  `json:"count"`
	PoolIDs     []string             `json:"pool_ids"`
	EligibleIDs []string             `json:"eligible_ids"`
	ReliedOn    []DecisionSourceView `json:"relied_on"`
}

// DecisionsResponse is a bounded page of immutable attempt evidence.
type DecisionsResponse struct {
	Version    int            `json:"version"`
	ChangeID   string         `json:"change_id"`
	Decisions  []DecisionView `json:"decisions"`
	NextCursor int64          `json:"next_cursor"`
}

type decisionSource interface {
	ChangeDecisions(context.Context, store.QueueKey, queue.ChangeID, uint64, int64, int) ([]store.DecisionRecord, int64, bool, error)
}

func serveDecisions(ctx context.Context, w http.ResponseWriter, source Source, key store.QueueKey, c changeRequest, sanitizer *diagnostic.Sanitizer) {
	reader, ok := source.(decisionSource)
	if !ok {
		fail(w, 503, "unavailable", "retained decision reader unavailable")
		return
	}
	records, next, found, err := reader.ChangeDecisions(ctx, key, queue.ChangeID(c.id), c.revision, c.after, c.limit)
	if err != nil {
		fail(w, 503, "unavailable", "retained decisions could not be read")
		return
	}
	if !found {
		fail(w, 404, "not-found", "change has no recorded history in this queue")
		return
	}
	response := DecisionsResponse{Version: Version, ChangeID: c.id, Decisions: []DecisionView{}, NextCursor: next}
	for _, r := range records {
		response.Decisions = append(response.Decisions, decisionView(r, sanitizer))
	}
	writeBoundedChange(w, response)
}

func decisionView(r store.DecisionRecord, sanitizer *diagnostic.Sanitizer) DecisionView {
	d := r.Evidence.Decision
	v := DecisionView{EligibilityEventID: r.Evidence.EligibilityEventID, EventID: r.EventID, SettlementEventID: r.SettlementEventID, BatchID: d.BatchID, AttemptID: d.AttemptID, Variant: d.Variant, Outcome: r.Outcome, TestedSHA: d.TestedSHA, IntendedLandingSHA: d.LandedSHA, LandedSHA: brief.NotRecorded, EligibilityReadID: brief.NotRecorded, BriefDigest: brief.NotRecorded, IntentDigest: brief.NotRecorded, PolicyDigest: brief.NotRecorded}
	if r.Outcome == "landed" {
		v.LandedSHA = r.LandedSHA
	}
	if d.Remedy != nil {
		v.ChangeID = string(d.Remedy.ChangeID)
		v.Revision = uint64(d.Remedy.Revision)
	}
	if a := d.Approval; a != nil {
		v.Accountability = &AccountabilityView{Status: brief.NotRecorded, Actor: &IdentityView{ID: a.Actor.ID, Login: sanitizer.Sanitize(a.Actor.Login), Kind: string(a.Actor.Kind)}, Author: &IdentityView{ID: a.Author.ID, Login: sanitizer.Sanitize(a.Author.Login), Kind: string(a.Author.Kind)}, SubmitterID: recorded(a.SubmitterID), Humans: []IdentityView{}}
		if a.AccountabilityRecorded {
			v.Accountability.Status = "recorded"
		}
		for _, h := range a.AccountableHumans {
			v.Accountability.Humans = append(v.Accountability.Humans, IdentityView{ID: h.ID, Login: sanitizer.Sanitize(h.Login), Kind: string(h.Kind)})
		}
		v.ChangeID = string(a.Basis.ChangeID)
		v.Revision = uint64(a.Basis.Revision)
		v.EligibilityReadID = a.EligibilityReadID
		v.BriefDigest = a.Identity.BriefDigest
		v.IntentDigest = a.Identity.IntentDigest
		v.PolicyDigest = a.Identity.PolicyDigest
		v.Sources = []DecisionSourceView{}
		v.Requirements = []DecisionRequirementView{}
		source := func(s queue.LandingApprovalSource) DecisionSourceView {
			return DecisionSourceView{s.UserID, string(s.Channel), s.SourceID, s.ReadID, recorded(s.ReceiptID), recorded(s.IntentDigest), recorded(s.BriefDigest)}
		}
		for _, s := range a.Sources {
			row := DecisionSourceView{UserID: s.Reviewer.ID, Channel: string(s.Source.Channel), SourceID: s.Source.ID, ReadID: s.Basis.ReadID, ReceiptID: brief.NotRecorded, IntentDigest: brief.NotRecorded, BriefDigest: brief.NotRecorded}
			if s.Receipt != nil {
				row.ReceiptID = s.Source.ID
				row.IntentDigest = s.Receipt.Supplied.IntentDigest
				row.BriefDigest = s.Receipt.Supplied.BriefDigest
			}
			v.Sources = append(v.Sources, row)
		}
		for _, r := range a.Requirements {
			row := DecisionRequirementView{Cause: string(r.Cause), Path: sanitizer.Sanitize(r.Path), Side: string(r.Side), Selector: string(r.Selector), Group: sanitizer.Sanitize(r.Group), Human: r.Human, Receipt: r.Receipt, Count: r.Count, PoolIDs: r.PoolIDs, EligibleIDs: r.EligibleIDs, ReliedOn: []DecisionSourceView{}}
			for _, s := range r.ReliedOn {
				row.ReliedOn = append(row.ReliedOn, source(s))
			}
			v.Requirements = append(v.Requirements, row)
		}
	}
	return v
}

func recorded(s string) string {
	if s == "" {
		return brief.NotRecorded
	}
	return s
}

func writeBoundedChange(w http.ResponseWriter, value any) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > brief.MaxViewBytes {
		fail(w, 503, "unavailable", "change evidence exceeds response limit; request a smaller page")
		return
	}
	_, _ = w.Write(append(raw, '\n'))
}
