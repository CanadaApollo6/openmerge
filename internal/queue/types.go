package queue

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// Config controls a queue with one active batch. ResolveAfter is the wait before fencing and
// reading an uncertain landing; it never makes the landing a failure.
type Config struct {
	// BatchMaxSize defaults to 1; BatchMaxWait bounds formation of partial batches.
	BatchMaxSize int
	BatchMaxWait time.Duration
	MergeMethod  MergeMethod
	CheckTimeout time.Duration
	ResolveAfter time.Duration
	// RefreshInterval bounds the delay before reconciling checks; nonpositive defaults to 1m.
	RefreshInterval time.Duration
	// BuildRetries is the consecutive non-conflict failure limit; zero defaults to 3.
	BuildRetries int
	// BuildBackoff is the first retry delay, doubled for each consecutive failure.
	// A nonpositive duration disables the delay, but not the failure limit.
	BuildBackoff time.Duration
}

// MergeMethod selects the shape of the commit built by the executor.
type MergeMethod string

// Supported merge methods preserve the exact tested commit at landing.
const (
	Merge  MergeMethod = "merge"
	Squash MergeMethod = "squash"
	Rebase MergeMethod = "rebase"
)

// State is a replayable projection for one base branch. Its zero value is empty.
type State struct {
	BaseBranch string
	BaseSHA    string
	Entries    []Entry
	Active     *Batch
	pending    []pendingGroup
	Fence      string

	nextBatch     uint64
	nextAdmission uint64
	revision      uint64
	observations  map[int]PRObserved
	dequeues      map[int]Dequeue
	effects       []Action
	acknowledged  map[string]effectScope
	checks        map[string]retainedCheck
	testedBatches map[string]string
	testedOrder   []string // insertion order of testedBatches, oldest first
	buildRetry    buildRetryState
	actionPoison  actionPoisonState
}

// Entry records the head admitted to the queue, independently of later observations.
type Entry struct {
	PR               int
	AdmissionID      uint64
	HeadSHA          string
	EnqueuedAt       time.Time
	DequeueRequested bool
}

// Batch is the unit of testing and landing.
type Batch struct {
	FromBisection    bool
	ID               string
	PRs              []BatchPR
	BaseSHA          string
	TestRef          string
	TestedSHA        string
	Method           MergeMethod
	Phase            Phase
	StartedTestingAt time.Time
	LandingAttempt   LandingAttempt

	checksPassed      bool
	checksRefreshedAt time.Time
	checkRefreshes    uint64
}

// BatchPR pins the exact head used to build a test ref.
type BatchPR struct {
	PR      int
	HeadSHA string
}

// Phase identifies the operation whose outcome a batch is waiting for.
type Phase string

// Batch phases distinguish cancellable work from an uncertain landing.
const (
	Building Phase = "building"
	Testing  Phase = "testing"
	Landing  Phase = "landing"
)

// LandingAttempt is the durable in-flight record, recorded before calling the forge.
type LandingAttempt struct {
	ID         string
	FenceToken string
	StartedAt  time.Time
}

// DequeueReason values are a stable public contract for agents.
type DequeueReason string

// Stable dequeue reasons are suitable for machine-readable outcomes.
const (
	NotEligible   DequeueReason = "not-eligible"
	Untrusted     DequeueReason = "untrusted"
	HeadChanged   DequeueReason = "head-changed"
	ChecksFailed  DequeueReason = "checks-failed"
	ChecksTimeout DequeueReason = "checks-timeout"
	Conflict      DequeueReason = "conflict"
	Closed        DequeueReason = "closed"
	Requested     DequeueReason = "requested"
)

// Event is a normalized observation, command, or action result.
type Event interface{ queueEvent() }

// PRObserved carries eligibility and exact-head trust evaluated outside the queue.
type PRObserved struct {
	PR                int
	HeadSHA           string
	Eligible, Trusted bool
}

// BaseMoved invalidates test work when the SHA changes, but cannot settle a landing.
// Observing the current BaseSHA again is a no-op, including for build retries.
type BaseMoved struct{ SHA string }

// ChecksCompleted is correlated by tested SHA, independently of webhook order.
type ChecksCompleted struct {
	TestedSHA string
	Success   bool
}

// ChecksReopened revokes a terminal result without restarting the check deadline.
type ChecksReopened struct {
	TestedSHA string
	At        time.Time
}

// ChecksRefreshed records a forge read after ChecksCompleted or ChecksReopened.
// It is correlated by both batch and tested SHA, even when checks are still pending.
type ChecksRefreshed struct {
	BatchID, TestedSHA string
	At                 time.Time
}

// PRClosed revokes the observation so a closed PR cannot be admitted again.
type PRClosed struct{ PR int }

// EnqueueRequested uses the latest observation, never the caller's view of trust.
type EnqueueRequested struct {
	PR int
	At time.Time
}

// DequeueRequested defers acknowledgement when landing has an uncertain outcome.
type DequeueRequested struct{ PR int }

// ResumeRequested clears build failures and poison pauses and resumes the queue.
type ResumeRequested struct{}

// BatchStarted durably records the planner's composition before any build I/O.
type BatchStarted struct {
	BatchID, BaseSHA string
	PRs              []BatchPR
	At               time.Time
}

// BatchFailed records a planner timeout after a final checks refresh.
type BatchFailed struct {
	BatchID string
	Reason  DequeueReason
}

// TestRefBuilt records the tested SHA, actual method, and origin of its check timer.
type TestRefBuilt struct {
	BatchID, TestedSHA string
	At                 time.Time
	Method             MergeMethod
}

// TestRefBuildFailed distinguishes a PR conflict from a retryable build failure.
type TestRefBuildFailed struct {
	BatchID  string
	Conflict bool
	At       time.Time
}

// LandingStarted must be recorded before the executor attempts the fenced write.
type LandingStarted struct {
	BatchID, AttemptID, FenceToken string
	At                             time.Time
}

// Landed records the definitive base and fence after the atomic landing push.
type Landed struct{ BatchID, NewBaseSHA, NewFence string }

// LandingAbandoned certifies that the advanced fence prevents any late landing.
type LandingAbandoned struct{ BatchID, NewFence string }

// Dequeued echoes the admission and, for a batch-scoped timeout, the BatchID.
type Dequeued struct {
	PR          int
	AdmissionID uint64
	Reason      DequeueReason
	BatchID     string
}

// ActionCompleted acknowledges ReportStatus, MarkLanded, and DeleteRef effects.
type ActionCompleted struct{ Key string }

// ActionPoisoned pauses the queue after repeated corruption of one action key.
type ActionPoisoned struct{ Key, Reason string }

func (BatchStarted) queueEvent() {}
func (BatchFailed) queueEvent()  {}

func (PRObserved) queueEvent()         {}
func (BaseMoved) queueEvent()          {}
func (ChecksCompleted) queueEvent()    {}
func (ChecksReopened) queueEvent()     {}
func (ChecksRefreshed) queueEvent()    {}
func (PRClosed) queueEvent()           {}
func (EnqueueRequested) queueEvent()   {}
func (DequeueRequested) queueEvent()   {}
func (ResumeRequested) queueEvent()    {}
func (TestRefBuilt) queueEvent()       {}
func (TestRefBuildFailed) queueEvent() {}
func (LandingStarted) queueEvent()     {}
func (Landed) queueEvent()             {}
func (LandingAbandoned) queueEvent()   {}
func (Dequeued) queueEvent()           {}
func (ActionCompleted) queueEvent()    {}
func (ActionPoisoned) queueEvent()     {}

// Action describes an intended effect. Executors deduplicate by Key and record
// its result as an Event; they must revalidate stale actions under ADR 0002.
type Action interface {
	queueAction()
	Key() string
	Validate() error
}

// BuildTestRef pins the input heads, base, and merge method at planning time.
// The executor must include BatchID in the top commit (for example, an
// OpenMerge-Batch trailer), so each tested SHA identifies exactly one batch.
type BuildTestRef struct {
	BatchID, BaseSHA string
	PRs              []BatchPR
	Ref              string
	Method           MergeMethod
}

// FailBatch records a timeout decision so replay can split a multi-PR batch.
type FailBatch struct {
	BatchID string
	Reason  DequeueReason
}

func (FailBatch) queueAction() {}

// Key pins the recorded failure to one active batch.
func (a FailBatch) Key() string { return "batch/" + a.BatchID + "/fail/" + string(a.Reason) }

// RefreshChecks reads forge CI status for TestedSHA. The executor first appends
// ChecksCompleted if terminal or ChecksReopened otherwise, then always appends
// ChecksRefreshed with the read time. A failed read records no refresh.
type RefreshChecks struct {
	BatchID, TestedSHA string
	epoch              uint64
}

// Land requires an atomic compare-and-swap on both the base and fence.
type Land struct {
	BatchID, ExpectedBaseSHA, TestedSHA, Ref, FenceToken string
	PRs                                                  []BatchPR
	Method                                               MergeMethod
}

// ResolveLanding carries the pinned commit and durable attempt fence so the
// executor can fence and read the outcome without consulting mutable queue state.
type ResolveLanding struct{ BatchID, AttemptID, TestedSHA, FenceToken string }

// DeleteRef is repeatable cleanup, acknowledged separately from queue transitions.
type DeleteRef struct{ Ref string }

// Dequeue remains pending until the executor records a Dequeued result.
type Dequeue struct {
	HeadSHA     string
	PR          int
	AdmissionID uint64
	Reason      DequeueReason
	BatchID     string
	scope       string
}

// ReportStatus makes admission failures and definitive outcomes visible to callers.
type ReportStatus struct {
	PR                              int
	HeadSHA, Status, Reason, Detail string
	revision                        uint64
	admission                       uint64
}

func (BuildTestRef) queueAction()   {}
func (RefreshChecks) queueAction()  {}
func (Land) queueAction()           {}
func (ResolveLanding) queueAction() {}
func (DeleteRef) queueAction()      {}
func (Dequeue) queueAction()        {}
func (ReportStatus) queueAction()   {}

// MarkLanded closes PRs whose heads are not reachable after squash or rebase.
type MarkLanded struct {
	PR                 int
	HeadSHA, LandedSHA string
}

func (MarkLanded) queueAction() {}

// Key pins closing to the PR head and the exact landed commit.
func (a MarkLanded) Key() string {
	return fmt.Sprintf("pr/%d/head/%s/landed/%s", a.PR, a.HeadSHA, a.LandedSHA)
}

// Key pins the unique batch, planned method, base and ordered composition.
func (a BuildTestRef) Key() string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s", len(a.BaseSHA), a.BaseSHA)
	for _, pr := range a.PRs {
		_, _ = fmt.Fprintf(h, "/%d/%d:%s", pr.PR, len(pr.HeadSHA), pr.HeadSHA)
	}
	return fmt.Sprintf("batch/%s/build/%s/%x", a.BatchID, a.Method, h.Sum(nil))
}

// Key stays stable until a refresh result advances this batch's refresh epoch.
func (a RefreshChecks) Key() string {
	return fmt.Sprintf("batch/%s/checks/refresh/%d", a.BatchID, a.epoch)
}

// Key identifies the start action. The executor mints a separate random attempt
// ID; once LandingStarted is durable, only ResolveLanding may be executed.
func (a Land) Key() string { return "batch/" + a.BatchID + "/land" }

// Key correlates resolution retries with the durable landing attempt.
func (a ResolveLanding) Key() string {
	return "batch/" + a.BatchID + "/resolve/" + a.AttemptID
}

// Key uses the unique batch ref, so acknowledged cleanup need never be repeated.
func (a DeleteRef) Key() string { return "ref/" + a.Ref + "/delete" }

// Key distinguishes a later dequeue of the same PR from retries of this one.
func (a Dequeue) Key() string {
	return fmt.Sprintf("pr/%d/admission/%d/dequeue/%s/%s", a.PR, a.AdmissionID, a.scope, a.Reason)
}

// Key distinguishes separate outcomes while keeping retries of one report stable.
func (a ReportStatus) Key() string {
	return fmt.Sprintf("pr/%d/admission/%d/report/%d/%s", a.PR, a.admission, a.revision, a.Status)
}
