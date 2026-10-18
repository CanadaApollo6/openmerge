package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ApprovalBasis pins the read independently of the actor and source action.
// No producer is wired before Stage A 11a/11b.
type ApprovalBasis struct {
	ChangeID ChangeID       `json:"change_id"`
	Revision ChangeRevision `json:"revision"`
	Head     string         `json:"head"`
	ReadID   string         `json:"read_id"`
	At       time.Time      `json:"at"`
}

// Validate requires a durable change/revision/head and identifiable read.
func (b ApprovalBasis) Validate() error {
	if b.ChangeID.Validate() != nil || b.Revision == 0 || !forge.ValidDiffObjectID(b.Head) || b.ReadID == "" || b.At.IsZero() {
		return fmt.Errorf("invalid approval read basis")
	}
	return nil
}
func validSourceID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 63)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
func validActorID(id string) bool {
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "github.com:user:"), 10, 64)
	return err == nil && classification.GitHubUserID(n) == id
}
func validIdentity(i cp.Identity) bool {
	return validActorID(i.ID) && i.Login != "" && (i.Kind == cp.HumanAccount || i.Kind == cp.AgentAccount)
}

// ChangeAccountabilityObserved keeps authentication, PR authorship and
// accountability separate. An absent human list is explicitly not recorded.
type ChangeAccountabilityObserved struct {
	Basis        ApprovalBasis `json:"basis"`
	PolicyDigest string        `json:"policy_digest"`
	Actor        cp.Identity   `json:"actor"`
	Author       cp.Identity   `json:"author"`
	SubmitterID  string        `json:"submitter_id"`
	Humans       []cp.Identity `json:"humans"`
	Recorded     bool          `json:"recorded"`
	Reason       string        `json:"reason"`
}

func (ChangeAccountabilityObserved) queueEvent() {}

// Validate rejects conflated or unrecorded identities disguised as humans.
func (e ChangeAccountabilityObserved) Validate() error {
	if e.PolicyDigest == "" || e.Basis.Validate() != nil || !validIdentity(e.Actor) || !validIdentity(e.Author) || (e.SubmitterID != "" && !validActorID(e.SubmitterID)) || e.Humans == nil || e.Recorded != (len(e.Humans) > 0) || (!e.Recorded && e.Reason == "") {
		return fmt.Errorf("invalid accountability")
	}
	for _, h := range e.Humans {
		if !validIdentity(h) || h.Kind != cp.HumanAccount {
			return fmt.Errorf("invalid accountable human")
		}
	}
	return nil
}

// Accountability applies configured stable-ID mappings before account kind.
// It grants no permissions or fork trust. No git identity is an input.
func Accountability(b ApprovalBasis, actor, author cp.Identity, mappings map[string][]cp.Identity, policyDigest string) ChangeAccountabilityObserved {
	if _, mapped := mappings[actor.ID]; mapped {
		actor.Kind = cp.AgentAccount
	}
	e := ChangeAccountabilityObserved{PolicyDigest: policyDigest, Basis: b, Actor: actor, Author: author, Humans: []cp.Identity{}}
	if humans, ok := mappings[author.ID]; ok {
		e.Author.Kind = cp.AgentAccount
		e.Humans = append(e.Humans, humans...)
	} else if author.Kind == cp.HumanAccount {
		e.Humans = append(e.Humans, author)
	}
	e.Recorded = len(e.Humans) > 0
	if !e.Recorded {
		e.Reason = "accountable human not recorded"
	}
	return e
}

// ChangeIntentObserved stores canonical text, not an observation-time binding
// silently attached to a native review.
type ChangeIntentObserved struct {
	Basis  ApprovalBasis `json:"basis"`
	Title  string        `json:"title"`
	Body   string        `json:"body"`
	Digest string        `json:"digest"`
}

func (ChangeIntentObserved) queueEvent() {}
func normalizeIntent(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
}

// Intent normalizes line endings and pins a domain-separated title/body digest.
func Intent(b ApprovalBasis, title, body string) ChangeIntentObserved {
	e := ChangeIntentObserved{Basis: b, Title: normalizeIntent(title), Body: normalizeIntent(body)}
	raw, _ := json.Marshal([]string{"openmerge/intent/v1", e.Title, e.Body})
	sum := sha256.Sum256(raw)
	e.Digest = hex.EncodeToString(sum[:])
	return e
}

// Validate checks canonical text against its recorded digest.
func (e ChangeIntentObserved) Validate() error {
	if e.Basis.Validate() != nil || e != Intent(e.Basis, e.Title, e.Body) {
		return fmt.Errorf("invalid normalized intent/digest")
	}
	return nil
}

// ReviewStanceObserved retains acquisition provenance beyond StanceSource.
// Basis is the read revision; the fold independently pins native attribution.
// Receipt commands and their acceptance records belong to PR 7a.
type ReviewStanceObserved struct {
	Basis              ApprovalBasis           `json:"basis"`
	Reviewer           cp.Identity             `json:"reviewer"`
	Stance             cp.Stance               `json:"stance"`
	SourceKind         forge.NativeTimeSource  `json:"source_kind"`
	SourceTime         forge.NativeTime        `json:"source_time"`
	Observation        forge.NativeObservation `json:"observation"`
	OriginalSubmission forge.NativeTime        `json:"original_submission"`
}

func (ReviewStanceObserved) queueEvent() {}

// Validate checks source identity, time provenance and revision/head bindings.
func (e ReviewStanceObserved) Validate() error {
	s := e.Stance
	src := s.Source
	if e.Basis.Validate() != nil || !validIdentity(e.Reviewer) || s.UserID != e.Reviewer.ID || s.Revision != uint64(e.Basis.Revision) || !forge.ValidDiffObjectID(s.Head) || !validSourceID(src.ID) || strconv.FormatUint(src.Sequence, 10) != src.ID || (src.ActingUserID != "" && !validActorID(src.ActingUserID)) || (s.Kind != cp.Approve && s.Kind != cp.Withdraw && s.Kind != cp.RequestChanges) {
		return fmt.Errorf("invalid stance")
	}
	if src.Channel != cp.NativeReview && src.Channel != cp.CommentCommand {
		return fmt.Errorf("invalid stance channel")
	}
	if e.SourceTime.Precision < 0 || e.SourceTime.Uncertainty < 0 || !src.UpperTimeBound.IsZero() && src.UpperTimeBound.Before(src.Time) || src.TimeKnown && (src.Time.IsZero() || src.Precision != cp.ExactTime) {
		return fmt.Errorf("invalid source time")
	}
	if src.Precision != "" && src.Precision != cp.ExactTime && src.Precision != cp.UpperBoundTime && src.Precision != cp.ObservationFallback {
		return fmt.Errorf("invalid precision")
	}
	if src.Channel == cp.CommentCommand && e.SourceKind != forge.CommentCreationTime || src.Channel == cp.NativeReview && e.SourceKind == forge.CommentCreationTime {
		return fmt.Errorf("source channel/time kind mismatch")
	}
	if src.Channel == cp.NativeReview && src.DismissedReviewID == "" && (s.Kind == cp.Withdraw || e.SourceKind != forge.ReviewSubmissionTime) || e.SourceKind == forge.ObservationTimeFallback && (src.TimeKnown || src.Precision != cp.ObservationFallback) || src.Channel == cp.CommentCommand && src.ActingUserID != s.UserID || src.DismissedReviewID == "" && src.ActingUserID != s.UserID {
		return fmt.Errorf("invalid source authority")
	}
	if src.Channel == cp.NativeReview && s.Receipt != nil {
		return fmt.Errorf("native intent/brief not recorded")
	}
	if src.Channel == cp.CommentCommand && (s.Head != e.Basis.Head || s.Kind == cp.Approve && (s.Receipt == nil || !s.Receipt.Recorded() || s.Receipt.ChangeID != string(e.Basis.ChangeID) || s.Receipt.Revision != s.Revision || s.Receipt.Head != s.Head)) {
		return fmt.Errorf("invalid command binding")
	}
	if e.SourceKind != e.SourceTime.Source || !src.Time.Equal(e.SourceTime.Time) || !src.UpperTimeBound.Equal(e.SourceTime.UpperBound) {
		return fmt.Errorf("inconsistent time provenance")
	}
	switch e.SourceKind {
	case forge.ReviewSubmissionTime, forge.CommentCreationTime, forge.ReviewDismissalTime, forge.TimelineDismissalTime, forge.ObservationTimeFallback:
	default:
		return fmt.Errorf("invalid source kind")
	}
	if src.DismissedReviewID == "" && e.OriginalSubmission != (forge.NativeTime{}) || src.DismissedReviewID != "" && (e.OriginalSubmission.Source != forge.ReviewSubmissionTime || e.OriginalSubmission.Precision < 0 || e.OriginalSubmission.Uncertainty < 0 || !e.OriginalSubmission.UpperBound.IsZero() && e.OriginalSubmission.UpperBound.Before(e.OriginalSubmission.Time)) {
		return fmt.Errorf("invalid original review attribution evidence")
	}
	if src.DismissedReviewID != "" && (!validSourceID(src.DismissedReviewID) || src.Channel != cp.NativeReview || s.Kind != cp.Withdraw || e.SourceKind == forge.ReviewSubmissionTime || e.SourceKind == forge.CommentCreationTime) {
		return fmt.Errorf("invalid dismissal source")
	}
	return nil
}

// NativeStance maps PR 3b DTOs without using submission time for a dismissal.
func NativeStance(b ApprovalBasis, r forge.NativeReview) (ReviewStanceObserved, bool) {
	if r.ID <= 0 || r.Author.Kind == forge.UnknownAccount || r.Author.Kind == "" {
		return ReviewStanceObserved{}, false
	}
	id := classification.GitHubUserID(r.Author.ID)
	kind := cp.AgentAccount
	if r.Author.Kind == forge.UserAccount {
		kind = cp.HumanAccount
	}
	e := ReviewStanceObserved{Basis: b, Reviewer: cp.Identity{ID: id, Login: r.Author.Login, Kind: kind}, Observation: r.Observation, SourceTime: r.Submitted}
	e.Stance = cp.Stance{UserID: id, Revision: uint64(b.Revision), Head: r.CommitID, Source: cp.StanceSource{Channel: cp.NativeReview, ID: strconv.FormatInt(r.ID, 10), Sequence: uint64(r.ID), ActingUserID: id}}
	switch r.State {
	case forge.ReviewApproved:
		e.Stance.Kind = cp.Approve
	case forge.ReviewChangesRequested:
		e.Stance.Kind = cp.RequestChanges
	case forge.ReviewDismissed:
		if r.Dismissal == nil {
			return e, false
		}
		d := r.Dismissal
		e.Stance.Kind = cp.Withdraw
		e.OriginalSubmission = r.Submitted
		e.SourceTime = d.Time
		e.Observation = d.Observation
		e.Stance.Source.DismissedReviewID = strconv.FormatInt(r.ID, 10)
		e.Stance.Source.ActingUserID = classification.GitHubUserID(d.Actor.ID)
		if d.EventID > 0 {
			e.Stance.Source.ID = strconv.FormatInt(d.EventID, 10)
			e.Stance.Source.Sequence = uint64(d.EventID)
		}
	default:
		return e, false
	}
	e.SourceKind = e.SourceTime.Source
	e.Stance.Source.Time = e.SourceTime.Time
	e.Stance.Source.UpperTimeBound = e.SourceTime.UpperBound
	if e.SourceKind == forge.ObservationTimeFallback {
		e.Stance.Source.Precision = cp.ObservationFallback
	} else if !e.SourceTime.Time.IsZero() {
		e.Stance.Source.TimeKnown = true
		e.Stance.Source.Precision = cp.ExactTime
	}
	return e, e.Validate() == nil
}

// ApprovalEligibilityObserved records the exact known/unknown decision inputs.
// Stored Fresh=true is audit evidence, never permission for a later decision.
type ApprovalEligibilityObserved struct {
	Basis      ApprovalBasis             `json:"basis"`
	Actors     map[string]cp.Actor       `json:"actors"`
	Groups     map[string]cp.IdentitySet `json:"groups"`
	OwnerPools map[string]cp.IdentitySet `json:"owner_pools"`
	Known      bool                      `json:"known"`
	Reasons    []string                  `json:"reasons"`
}

func (ApprovalEligibilityObserved) queueEvent() {}

// Validate preserves unknown observations and canonical stable actor IDs.
func (e ApprovalEligibilityObserved) Validate() error {
	if e.Basis.Validate() != nil || e.Actors == nil || e.Groups == nil || e.OwnerPools == nil || e.Reasons == nil || !e.Known && len(e.Reasons) == 0 {
		return fmt.Errorf("invalid eligibility")
	}
	for id, a := range e.Actors {
		if !validIdentity(a.Identity) || a.Identity.ID != id || a.Grant.SubjectID != id || (a.Grant.ReturnedID != "" && !validActorID(a.Grant.ReturnedID)) || a.Grant.ReturnedID == "" && (a.Grant.Fresh || a.Grant.Write != cp.Unknown) || a.Grant.Write > cp.True {
			return fmt.Errorf("invalid actor/grant identity")
		}
	}
	for label := range e.OwnerPools {
		if !strings.HasPrefix(label, "owned:user:") && !strings.HasPrefix(label, "owned:team:") || !cp.ValidClassificationLabel(label) {
			return fmt.Errorf("invalid owner pool token")
		}
	}
	pools := make([]cp.IdentitySet, 0, len(e.Groups)+len(e.OwnerPools))
	for _, g := range e.Groups {
		pools = append(pools, g)
	}
	for _, g := range e.OwnerPools {
		pools = append(pools, g)
	}
	for _, g := range pools {
		if g.IDs == nil {
			return fmt.Errorf("membership IDs must not be null")
		}
		for _, id := range g.IDs {
			if !validActorID(id) {
				return fmt.Errorf("invalid membership ID")
			}
		}
		if (!g.Resolved || !g.Fresh) && g.Reason == "" {
			return fmt.Errorf("unknown membership needs reason")
		}
	}
	return nil
}
