package changepolicy

import "time"

// SourceChannel separates native review ordering from authenticated commands.
type SourceChannel string

// Source channels have independent ordering.
const (
	NativeReview   SourceChannel = "native_review"
	CommentCommand SourceChannel = "comment_command"
)

// TimePrecision identifies the authority and precision of a source timestamp.
// The zero value is unknown; observation fallback is a conservative bound.
type TimePrecision string

// Source time precision values are recorded by the acquisition/fold boundary.
const (
	ExactTime           TimePrecision = "exact"
	UpperBoundTime      TimePrecision = "upper_bound"
	ObservationFallback TimePrecision = "observation_fallback"
)

// StanceSource retains the source's identity, ordering and time evidence.
// UpperTimeBound is PR 3b/6's pinned conservative withdrawal barrier when the
// exact action time is unavailable. The fold, not this evaluator, orders stances.
type StanceSource struct {
	Channel           SourceChannel
	ID                string
	Sequence          uint64
	Time              time.Time
	TimeKnown         bool
	UpperTimeBound    time.Time
	Precision         TimePrecision
	DismissedReviewID string
	ActingUserID      string
}

// StanceKind is the latest revision-attributed position supplied by PR 6.
type StanceKind string

// Stance kinds include both blocking and nonblocking withdrawals.
const (
	Approve        StanceKind = "approve"
	RequestChanges StanceKind = "request_changes"
	Withdraw       StanceKind = "withdraw"
)

// Stance is already folded and attributed to a durable revision. Native
// approvals have nil Receipt, never bindings inferred at observation time.
type Stance struct {
	UserID   string
	Kind     StanceKind
	Revision uint64
	Head     string
	Source   StanceSource
	Receipt  *ReceiptIdentity
}
