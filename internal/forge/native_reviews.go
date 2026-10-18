package forge

import (
	"context"
	"errors"
	"time"
)

// NativeReviewState preserves comment-only and pending reviews without treating
// either as approval or withdrawal. Unknown provider states fail acquisition.
type NativeReviewState string

// Native review states preserve the provider's submitted decision.
const (
	ReviewApproved         NativeReviewState = "approve"
	ReviewChangesRequested NativeReviewState = "request_changes"
	ReviewCommented        NativeReviewState = "comment"
	ReviewDismissed        NativeReviewState = "dismissed"
	ReviewPending          NativeReviewState = "pending"
)

// NativeTimeSource identifies the authority, independently of timestamp precision.
type NativeTimeSource string

// Source authorities distinguish action timestamps from fallback bounds.
const (
	ReviewSubmissionTime    NativeTimeSource = "review_submission"
	CommentCreationTime     NativeTimeSource = "comment_creation"
	ReviewDismissalTime     NativeTimeSource = "review_dismissal"
	TimelineDismissalTime   NativeTimeSource = "timeline_dismissal"
	ObservationTimeFallback NativeTimeSource = "observation_fallback"
)

// NativeTime retains source time and a conservative upper bound in the forge's
// clock. A zero Time is unknown; a zero UpperBound is not comparable. Precision
// is the timestamp resolution; Uncertainty is an additional clock allowance.
// An observation fallback is never the dismissed review's submission time.
type NativeTime struct {
	Time                   time.Time
	Source                 NativeTimeSource
	Precision, Uncertainty time.Duration
	UpperBound             time.Time
}

// NativeObservation identifies the response that supplied an item. ObservedAt
// is local acquisition time; ResponseDate is the forge clock (zero if absent or
// invalid). Local time alone cannot establish a comparable fallback bound.
type NativeObservation struct {
	ObservedAt, ResponseDate time.Time
}

// NativeDismissal attributes the blocking action separately from its reviewer.
// EventID and Actor may be unknown when the provider supplies no such evidence.
// ReviewID always identifies the dismissed review, including fallback evidence.
type NativeDismissal struct {
	ReviewID, EventID int64
	Actor             UserIdentity
	Time              NativeTime
	Observation       NativeObservation
}

// NativeReview is a source observation, not a folded stance or authorization.
// Submission time remains the original submission even after dismissal.
type NativeReview struct {
	ID          int64
	CommitID    string
	Author      UserIdentity
	Submitted   NativeTime
	State       NativeReviewState
	Dismissal   *NativeDismissal
	Observation NativeObservation
}

// NativeCommentKind separates issue-thread commands from inline review comments.
type NativeCommentKind string

// Comment namespaces are independent source ID spaces.
const (
	IssueComment  NativeCommentKind = "issue"
	ReviewComment NativeCommentKind = "review"
)

// NativeComment retains source identity and text for future authenticated command
// ingestion. ReviewID/CommitID are populated for inline review comments only.
type NativeComment struct {
	ID, ReviewID int64
	CommitID     string
	Author       UserIdentity
	Body         string
	Kind         NativeCommentKind
	Created      NativeTime
	Observation  NativeObservation
}

// NativeReviews is complete only on success. Arrays retain source order and
// duplicates; consumers must fold by stable IDs, not slice/observation order.
// The separate reads are not an atomic snapshot of concurrent forge edits.
type NativeReviews struct {
	Reviews  []NativeReview
	Comments []NativeComment
}

// Native acquisition errors always invalidate the entire observation.
var (
	ErrNativeReviewsUnavailable = errors.New("forge native reviews unavailable")
	ErrNativeReviewsIncomplete  = errors.New("forge native reviews incomplete")
)

// NativeReviewReader is an optional, dormant capability; it does not replace
// Driver's legacy Approval or Comment views. Success means every page of reviews,
// issue comments, review comments and required dismissal timeline was read.
// On error discard the entire result; missing/inaccessible data is never empty.
// Reads preserve settlement's reserved capacity.
//
// PR 6 must persist a fallback barrier at FIRST observation, keyed by review ID
// (and event ID when available), without advancing it on polls or restart. Only
// a distinct newer blocker may raise it. A clearing approval needs source time
// strictly after the pinned UpperBound and no newer blocker; ties/ambiguity
// block. Reobserve before clearing. If UpperBound is zero, require a new revision
// and a fresh review/receipt attributed to it. Preserve historical withdrawals.
// Conversion to changepolicy belongs to the consumer, never this package.
type NativeReviewReader interface {
	NativeReviews(context.Context, RepoRef, int) (NativeReviews, error)
}
