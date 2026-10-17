package queue

import (
	"fmt"
	"slices"
	"time"
)

// CheckRun identifies one producer's check attempt. Some forges retain the run
// ID across reruns; Attempt distinguishes those executions. State is normalized.
type CheckRun struct {
	ID      uint64 `json:"id"`
	Attempt uint64 `json:"attempt"`
	Name    string `json:"name"`
	Source  string `json:"source"`
	State   string `json:"state"`
}

// CheckRetryState is bounded to the active batch and survives projection copies
// and snapshots. Reopening checks never replaces its budget or timer origins.
type CheckRetryState struct {
	OriginalDeadline time.Time     `json:"original_deadline"`
	Deadline         time.Time     `json:"deadline"`
	RequestedAt      time.Time     `json:"requested_at"`
	Timeout          time.Duration `json:"timeout"`
	Requested        bool          `json:"requested"`
	Dispatched       bool          `json:"dispatched"`
	Unsupported      bool          `json:"unsupported"`
	Confirmed        bool          `json:"confirmed"`
	Detail           string        `json:"detail"`
	Generation       uint64        `json:"generation"`
	FailedRuns       []CheckRun    `json:"failed_runs"`
	ObservedRuns     []CheckRun    `json:"observed_runs"`
	RerunState       string        `json:"rerun_state"`
}

// ChecksObserved is the identified observation used only by retry-enabled
// queues. Existing ChecksCompleted events always retain their legacy meaning.
type ChecksObserved struct {
	BatchID          string        `json:"batch_id"`
	TestedSHA        string        `json:"tested_sha"`
	Runs             []CheckRun    `json:"runs"`
	Terminal         bool          `json:"terminal"`
	Success          bool          `json:"success"`
	OriginalDeadline time.Time     `json:"original_deadline"`
	Timeout          time.Duration `json:"timeout"`
}

// CheckRerunRequested consumes the sole retry before any remote mutation.
type CheckRerunRequested struct {
	BatchID          string        `json:"batch_id"`
	TestedSHA        string        `json:"tested_sha"`
	Generation       uint64        `json:"generation"`
	Runs             []CheckRun    `json:"runs"`
	OriginalDeadline time.Time     `json:"original_deadline"`
	At               time.Time     `json:"at"`
	Deadline         time.Time     `json:"deadline"`
	Timeout          time.Duration `json:"timeout"`
}

// CheckRerunDispatched records the capability's result, never a transient error.
type CheckRerunDispatched struct {
	BatchID     string `json:"batch_id"`
	TestedSHA   string `json:"tested_sha"`
	Generation  uint64 `json:"generation"`
	Unsupported bool   `json:"unsupported"`
	Detail      string `json:"detail"`
}

// ChecksFailureConfirmed is final for the recorded generation. It applies the
// same diagnosis as a legacy failed check or timeout, without restoring budget.
type ChecksFailureConfirmed struct {
	BatchID    string        `json:"batch_id"`
	TestedSHA  string        `json:"tested_sha"`
	Generation uint64        `json:"generation"`
	Reason     DequeueReason `json:"reason"`
}

// RecordCheckRerun persists the request and its deadline separately from dispatch.
type RecordCheckRerun struct {
	BatchID          string        `json:"batch_id"`
	TestedSHA        string        `json:"tested_sha"`
	Generation       uint64        `json:"generation"`
	Runs             []CheckRun    `json:"runs"`
	OriginalDeadline time.Time     `json:"original_deadline"`
	Timeout          time.Duration `json:"timeout"`
}

// RerunChecks dispatches the recorded, identified request. Its stable key is also
// the optional forge capability's idempotency key.
type RerunChecks struct {
	BatchID    string     `json:"batch_id"`
	TestedSHA  string     `json:"tested_sha"`
	TestRef    string     `json:"test_ref"`
	Generation uint64     `json:"generation"`
	Runs       []CheckRun `json:"runs"`
}

// ConfirmCheckFailure durably records the planner's final blame decision.
type ConfirmCheckFailure struct {
	BatchID    string        `json:"batch_id"`
	TestedSHA  string        `json:"tested_sha"`
	Generation uint64        `json:"generation"`
	Reason     DequeueReason `json:"reason"`
}

func (ChecksObserved) queueEvent()         {}
func (CheckRerunRequested) queueEvent()    {}
func (CheckRerunDispatched) queueEvent()   {}
func (ChecksFailureConfirmed) queueEvent() {}
func (RecordCheckRerun) queueAction()      {}
func (RerunChecks) queueAction()           {}
func (ConfirmCheckFailure) queueAction()   {}

// Key pins the one request to its pre-rerun checks generation.
func (a RecordCheckRerun) Key() string {
	return fmt.Sprintf("batch/%s/checks/rerun/request/%d", a.BatchID, a.Generation)
}

// Key includes the test ref so requests for separate bases cannot collide at the forge.
func (a RerunChecks) Key() string {
	return fmt.Sprintf("batch/%s/checks/rerun/dispatch/%d/%s", a.BatchID, a.Generation, a.TestRef)
}

// Key identifies one generation's final diagnosis.
func (a ConfirmCheckFailure) Key() string {
	return fmt.Sprintf("batch/%s/checks/confirm/%d/%s", a.BatchID, a.Generation, a.Reason)
}

func validateCheckRuns(runs []CheckRun) error {
	seen := make(map[string]bool)
	for _, r := range runs {
		if err := requireActionText("name", r.Name, "source", r.Source); err != nil {
			return err
		}
		key := r.Name + "\x00" + r.Source
		if seen[key] {
			return fmt.Errorf("runs must have distinct names and sources")
		}
		seen[key] = true
		switch r.State {
		case "pending", "success", "failure", "cancelled", "skipped", "neutral":
		default:
			return fmt.Errorf("invalid check state %q", r.State)
		}
	}
	return nil
}
func validateFailedCheckRuns(runs []CheckRun) error {
	if len(runs) == 0 {
		return fmt.Errorf("rerun requires failed run identities")
	}
	for _, r := range runs {
		if r.State == "pending" || r.State == "success" {
			return fmt.Errorf("rerun requires failed check states")
		}
	}
	return nil
}
func validateCheckTimer(timeout time.Duration, deadline time.Time) error {
	if timeout < 0 || (timeout == 0) != deadline.IsZero() {
		return fmt.Errorf("deadline must be set exactly when timeout is positive")
	}
	return nil
}

// Validate requires identified producers and a consistent original timer.
func (e ChecksObserved) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	if err := validateCheckTimer(e.Timeout, e.OriginalDeadline); err != nil {
		return err
	}
	return validateCheckRuns(e.Runs)
}

// Validate permits only failed check identities to consume a retry.
func (a RecordCheckRerun) Validate() error {
	if err := (ChecksObserved{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, Timeout: a.Timeout}).Validate(); err != nil {
		return err
	}
	return validateFailedCheckRuns(a.Runs)
}

// Validate fixes the rerun deadline at one timeout from the request.
func (e CheckRerunRequested) Validate() error {
	if err := (RecordCheckRerun{BatchID: e.BatchID, TestedSHA: e.TestedSHA, Generation: e.Generation, Runs: e.Runs, OriginalDeadline: e.OriginalDeadline, Timeout: e.Timeout}).Validate(); err != nil {
		return err
	}
	if e.At.IsZero() {
		return fmt.Errorf("at must not be zero")
	}
	if err := validateCheckTimer(e.Timeout, e.Deadline); err != nil {
		return err
	}
	if e.Timeout > 0 && !e.Deadline.Equal(e.At.Add(e.Timeout)) {
		return fmt.Errorf("rerun deadline must be one timeout from request")
	}
	return nil
}

// Validate binds capability results to their recorded batch and commit.
func (e CheckRerunDispatched) Validate() error {
	return requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA)
}

// Validate requires a test ref and the failed runs selected for dispatch.
func (a RerunChecks) Validate() error {
	if err := requireActionText("batch_id", a.BatchID, "tested_sha", a.TestedSHA, "test_ref", a.TestRef); err != nil {
		return err
	}
	if err := validateFailedCheckRuns(a.Runs); err != nil {
		return err
	}
	return validateCheckRuns(a.Runs)
}

// Validate restricts confirmation to check failures and check timeouts.
func (e ChecksFailureConfirmed) Validate() error {
	if e.Reason != ChecksFailed && e.Reason != ChecksTimeout {
		return fmt.Errorf("invalid confirmed failure reason %q", e.Reason)
	}
	return requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA)
}

// Validate pins the final diagnosis before it can be persisted.
func (a ConfirmCheckFailure) Validate() error { return ChecksFailureConfirmed(a).Validate() }

func (s *State) observeChecks(e ChecksObserved) {
	if e.Validate() != nil || !s.batchIn(e.BatchID, Testing) || s.Active.TestedSHA != e.TestedSHA {
		return
	}
	e.Runs = slices.Clone(e.Runs)
	for i := range e.Runs {
		e.Runs[i].Attempt = max(e.Runs[i].Attempt, 1)
	}
	b := s.Active
	if b.CheckRetry != nil && b.CheckRetry.Confirmed {
		return
	}
	if b.CheckRetry == nil {
		deadline := time.Time{}
		if e.Timeout > 0 {
			deadline = b.StartedTestingAt.Add(e.Timeout)
		}
		if !e.OriginalDeadline.Equal(deadline) {
			return
		}
		b.CheckRetry = &CheckRetryState{OriginalDeadline: e.OriginalDeadline, Timeout: e.Timeout}
	}
	r := b.CheckRetry
	if r.Unsupported {
		return
	}
	if r.Timeout != e.Timeout || !r.OriginalDeadline.Equal(e.OriginalDeadline) {
		return
	}
	// Preserve each producer's high-water mark even in a mixed stale read. Such
	// a read cannot authorize an aggregate result; fresh components also revoke
	// any request or landing planned from the older set until a coherent refresh.
	latest := latestObservedChecks(r.ObservedRuns, e.Runs)
	stale := olderCheckObservation(r.ObservedRuns, e.Runs)
	unproven := (r.Requested || e.Success && len(r.FailedRuns) > 0) && !r.NewAttempts(e.Runs)
	if e.Terminal && (stale || unproven) {
		if !slices.Equal(latest, r.ObservedRuns) {
			r.ObservedRuns = latest
			s.refreshChecks()
			r.RerunState = ""
			if r.Requested {
				r.RerunState = "requested"
				if r.Dispatched && r.NewAttempts(latest) {
					r.RerunState = "running"
				}
			}
		}
		return
	}
	r.ObservedRuns = latest
	if !e.Terminal {
		s.refreshChecks()
		if r.Requested && r.NewAttempts(e.Runs) {
			r.RerunState = "running"
		}
		return
	}
	wasPassed := b.checksPassed
	if wasPassed && !e.Success {
		// A failed run can arrive without a pending observation. Revoke an
		// already planned/preflighted Land even if the rerun later passes.
		s.refreshChecks()
	}
	b.checksPassed = e.Success
	if e.Success {
		if r.Requested || len(r.FailedRuns) > 0 {
			r.RerunState = "passed"
		}
		return
	}
	if !r.Requested {
		var failed []CheckRun
		for _, run := range e.Runs {
			if run.State != "success" && run.State != "pending" {
				failed = append(failed, run)
			}
		}
		if len(r.FailedRuns) > 0 && !slices.Equal(r.FailedRuns, failed) && !wasPassed {
			// A replacement failure before the request commits is a new
			// subject. Do not reuse an already published request payload/key.
			s.refreshChecks()
		}
		r.FailedRuns = failed
	}
	r.RerunState = "failed"
}

func olderCheckObservation(known, observed []CheckRun) bool {
	for _, run := range observed {
		for _, old := range known {
			if run.Name == old.Name && run.Source == old.Source && olderCheckAttempt(run, old) {
				return true
			}
		}
	}
	return false
}

func olderCheckAttempt(run, old CheckRun) bool {
	return run.ID < old.ID || run.ID == old.ID && max(run.Attempt, 1) < max(old.Attempt, 1)
}

// Keep one latest identity per required check/producer, rather than run history.
// Missing checks and older pending reads must not erase this high-water mark.
func latestObservedChecks(known, observed []CheckRun) []CheckRun {
	latest := slices.Clone(known)
	for _, run := range observed {
		index := slices.IndexFunc(latest, func(old CheckRun) bool { return old.Name == run.Name && old.Source == run.Source })
		if index < 0 {
			latest = append(latest, run)
		} else if !olderCheckAttempt(run, latest[index]) {
			latest[index] = run
		}
	}
	return latest
}

// NewAttempts reports whether every failed producer supplies a distinct newer attempt on the same tested
// commit and ref (the executor filters provenance before recording observations).
func (r CheckRetryState) NewAttempts(runs []CheckRun) bool {
	for _, old := range r.FailedRuns {
		if !slices.ContainsFunc(runs, func(run CheckRun) bool {
			return run.Name == old.Name && run.Source == old.Source && (run.ID > old.ID || run.ID == old.ID && max(run.Attempt, 1) > max(old.Attempt, 1))
		}) {
			return false
		}
	}
	return len(r.FailedRuns) > 0
}
func (s *State) requestCheckRerun(e CheckRerunRequested) {
	if e.Validate() != nil || !s.batchIn(e.BatchID, Testing) || s.Active.TestedSHA != e.TestedSHA || s.Active.checksGeneration != e.Generation {
		return
	}
	e.Runs = slices.Clone(e.Runs)
	for i := range e.Runs {
		e.Runs[i].Attempt = max(e.Runs[i].Attempt, 1)
	}
	r := s.Active.CheckRetry
	if r == nil || r.Requested || r.Confirmed || s.Active.checksPassed || !slices.Equal(r.FailedRuns, e.Runs) || r.Timeout != e.Timeout || !r.OriginalDeadline.Equal(e.OriginalDeadline) {
		return
	}
	r.Requested, r.RequestedAt, r.Deadline, r.Generation = true, e.At, e.Deadline, e.Generation
	r.RerunState = "requested"
	s.Active.needsRefresh = true
}
func (s *State) dispatchCheckRerun(e CheckRerunDispatched) {
	if e.Validate() != nil || !s.batchIn(e.BatchID, Testing) || s.Active.TestedSHA != e.TestedSHA {
		return
	}
	r := s.Active.CheckRetry
	if r == nil || !r.Requested || r.Dispatched || r.Generation != e.Generation || r.Confirmed {
		return
	}
	r.Dispatched, r.Unsupported, r.Detail = true, e.Unsupported, e.Detail
	if e.Unsupported {
		s.Active.checksPassed = false
		r.RerunState = "unsupported"
	}
}
func (s *State) confirmCheckFailure(e ChecksFailureConfirmed) {
	if e.Validate() != nil || !s.batchIn(e.BatchID, Testing) || s.Active.TestedSHA != e.TestedSHA || s.Active.checksGeneration != e.Generation || s.Active.checksPassed {
		return
	}
	if s.Active.CheckRetry == nil {
		s.Active.CheckRetry = &CheckRetryState{}
	}
	r := s.Active.CheckRetry
	if r.Confirmed {
		return
	}
	r.Confirmed = true
	if !r.Unsupported {
		r.RerunState = "failed"
	}
	s.failBatch(e.Reason)
	if e.Reason == ChecksTimeout && s.Active != nil && s.Active.ID == e.BatchID {
		// Legacy timer actions already carry a batch scope. A newly confirmed
		// singleton timeout needs that same stale-result protection.
		pr := s.Active.PRs[0].PR
		dequeue := s.dequeues[pr]
		dequeue.BatchID, dequeue.scope = e.BatchID, "batch/"+e.BatchID
		s.dequeues[pr] = dequeue
	}
}

func (b Batch) checkDeadline(cfg Config) (time.Time, bool) {
	if r := b.CheckRetry; r != nil {
		if r.Requested {
			return r.Deadline, r.Timeout > 0
		}
		return r.OriginalDeadline, r.Timeout > 0
	}
	return b.StartedTestingAt.Add(cfg.CheckTimeout), cfg.CheckTimeout > 0
}

// retryOldestBatch is the PR 3 oldest-batch hook. Config.ChecksRetryFailed is
// the effective retry limit shared by planner and executor; Queue.QueueConfig
// can enable it for max_depth > 1 by changing its policy assignment. Once the
// chain exists this hook must defer later batches' provisional failures.
func retryOldestBatch(_ Config, _ State) bool { return true }

func planCheckRetry(cfg Config, s State, now time.Time) (Action, bool) {
	b := s.Active
	r := b.CheckRetry
	if r == nil {
		return nil, false
	}
	confirm := func(reason DequeueReason) Action {
		return ConfirmCheckFailure{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Reason: reason}
	}
	if r.Unsupported || r.RerunState == "failed" && r.Requested {
		return confirm(ChecksFailed), true
	}
	if !r.Requested && r.RerunState == "failed" {
		if !retryOldestBatch(cfg, s) {
			// A later batch's failure is provisional until its predecessors settle.
			return nil, true
		}
		if cfg.ChecksRetryFailed != 1 || len(r.FailedRuns) == 0 {
			return confirm(ChecksFailed), true
		}
		// Terminal failure is authoritative even after the original deadline. Only
		// a qualifying pending refresh times out without requesting a retry.
		return RecordCheckRerun{BatchID: b.ID, TestedSHA: b.TestedSHA, Generation: b.checksGeneration, Runs: slices.Clone(r.FailedRuns), OriginalDeadline: r.OriginalDeadline, Timeout: r.Timeout}, true
	}
	deadline, enabled := b.checkDeadline(cfg)
	timedOut := enabled && now.After(deadline)
	if timedOut && !b.needsRefresh && b.checkRefreshes > 0 && !b.checksRefreshedAt.Before(deadline) {
		return confirm(ChecksTimeout), true
	}
	if r.Requested && !r.Dispatched && !timedOut {
		return RerunChecks{BatchID: b.ID, TestedSHA: b.TestedSHA, TestRef: b.TestRef, Generation: r.Generation, Runs: slices.Clone(r.FailedRuns)}, true
	}

	return nil, false
}
