package queue

import (
	"fmt"
	"slices"
	"time"
)

// CheckCancellationRequested pins best-effort cancellation to runs observed on
// a permanently retired speculative batch. It never supplies landing evidence.
type CheckCancellationRequested struct {
	BatchID   string     `json:"batch_id"`
	TestedSHA string     `json:"tested_sha"`
	TestRef   string     `json:"test_ref"`
	Runs      []CheckRun `json:"runs"`
	Reason    string     `json:"reason"`
}

func (CheckCancellationRequested) queueEvent() {}

// Validate requires identified runs and their immutable batch inputs.
func (e CheckCancellationRequested) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA, "test_ref", e.TestRef, "reason", e.Reason); err != nil {
		return err
	}
	if len(e.Runs) == 0 {
		return fmt.Errorf("cancellation requires identified runs")
	}
	for _, r := range e.Runs {
		if r.ID == 0 {
			return fmt.Errorf("cancellation requires native run IDs")
		}
	}
	return validateCheckRuns(e.Runs)
}

// RecordCheckCancellation persists intent before the first remote call.
type RecordCheckCancellation CheckCancellationRequested

func (RecordCheckCancellation) queueAction() {}

// Key is unique to the permanently retired batch.
func (a RecordCheckCancellation) Key() string { return "batch/" + a.BatchID + "/cancel/request" }

// Validate applies the same constraints as its durable intent.
func (a RecordCheckCancellation) Validate() error { return CheckCancellationRequested(a).Validate() }

// CancelChecks retries the same immutable targets after a crash or pending read.
// A successful cancel POST is only requested, never confirmation that CI stopped.
type CancelChecks CheckCancellationRequested

func (CancelChecks) queueAction() {}

// Key remains stable across idempotent cancellation retries.
func (a CancelChecks) Key() string { return "batch/" + a.BatchID + "/cancel" }

// Validate requires the durable intent's exact target identities.
func (a CancelChecks) Validate() error { return CheckCancellationRequested(a).Validate() }

// CheckCancellationReported records provider evidence. Confirmed runs have
// completed (including cancellation); unsupported leaves the runs to finish.
type CheckCancellationReported struct {
	BatchID     string     `json:"batch_id"`
	TestedSHA   string     `json:"tested_sha"`
	Unsupported bool       `json:"unsupported"`
	Detail      string     `json:"detail"`
	Confirmed   []CheckRun `json:"confirmed"`
}

func (CheckCancellationReported) queueEvent() {}

// Validate rejects pending runs presented as completion evidence.
func (e CheckCancellationReported) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	if e.Unsupported && e.Detail == "" {
		return fmt.Errorf("unsupported cancellation requires a reason")
	}
	for _, r := range e.Confirmed {
		if r.ID == 0 || r.State == "pending" {
			return fmt.Errorf("confirmation requires completed identified runs")
		}
	}
	return validateCheckRuns(e.Confirmed)
}

func sameCancellationRun(a, b CheckRun) bool {
	return a.ID == b.ID && max(a.Attempt, 1) == max(b.Attempt, 1) && a.Source == b.Source && a.Name == b.Name && a.WorkflowRunID == b.WorkflowRunID
}

func (s *State) retireChecks(b *Batch) {
	// Landing and landed batches both pass through discardBatch. They must never
	// create an intent. Only speculative admissions opt into this additive path.
	if !b.Speculative || b.Phase == Landing || b.TestedSHA == "" {
		return
	}
	runs := slices.Clone(b.ObservedRuns)
	if len(runs) == 0 && b.CheckRetry != nil {
		runs = slices.Clone(b.CheckRetry.ObservedRuns)
	}
	runs = slices.DeleteFunc(runs, func(r CheckRun) bool { return r.ID == 0 })
	if len(runs) == 0 {
		return
	}
	reason := b.InvalidationReason
	if reason == "" {
		reason = "batch-discarded"
	}
	for _, pr := range b.PRs {
		if d, ok := s.dequeues[pr.PR]; ok {
			reason = string(d.Reason)
			break
		}
	}
	s.addEffect(RecordCheckCancellation{BatchID: b.ID, TestedSHA: b.TestedSHA, TestRef: b.TestRef, Runs: runs, Reason: reason})
}
func (s *State) requestCancellation(e CheckCancellationRequested) {
	if e.Validate() != nil || s.Batch(e.BatchID) != nil {
		return
	}
	a := RecordCheckCancellation(e)
	for _, effect := range s.effects {
		pending, ok := effect.(RecordCheckCancellation)
		if ok && pending.Key() == a.Key() && pending.TestedSHA == e.TestedSHA && pending.TestRef == e.TestRef && pending.Reason == e.Reason && slices.Equal(pending.Runs, e.Runs) {
			s.completeEffect(a.Key())
			e.Runs = slices.Clone(e.Runs)
			s.addEffect(CancelChecks(e))
			return
		}
	}
}
func (s *State) reportCancellation(e CheckCancellationReported) {
	if e.Validate() != nil || s.Batch(e.BatchID) != nil {
		return
	}
	for i, effect := range s.effects {
		a, readOnly, _, ok := cancellationTarget(effect)
		if !ok || readOnly || a.BatchID != e.BatchID || a.TestedSHA != e.TestedSHA {
			continue
		}
		// Reports may contain disjoint subsets across calls. Remove each confirmed
		// identity durably so later calls only need to confirm the remaining targets.
		for _, run := range e.Confirmed {
			if !slices.ContainsFunc(a.Runs, func(target CheckRun) bool { return sameCancellationRun(target, run) }) {
				return
			}
		}
		a.Runs = slices.DeleteFunc(slices.Clone(a.Runs), func(run CheckRun) bool {
			return slices.ContainsFunc(e.Confirmed, func(c CheckRun) bool { return sameCancellationRun(run, c) })
		})
		if e.Unsupported || len(a.Runs) == 0 {
			s.completeEffect(effect.Key())
			if e.Unsupported {
				a.Runs = slices.DeleteFunc(a.Runs, func(r CheckRun) bool { return r.State != "pending" })
				if len(a.Runs) > 0 {
					s.addEffect(ObserveRetiredChecks(a))
				}
			}
		} else if retry, yes := effect.(RetryCheckCancellation); yes {
			retry.Cancellation = a
			s.effects[i] = retry
		} else {
			s.effects[i] = CancelChecks(a)
		}
		return
	}
}

// CancellationAttemptLimit bounds each mutation or read-only follow-up phase.
// Retries wait 30s, 1m, 2m, 4m, 8m, 16m and 30m (eight total attempts).
const CancellationAttemptLimit = 8

// RetryCheckCancellation is a durable, independently scheduled successor. Its
// unique key lets the previous action finish instead of occupying the oldest
// claimable row while other queue work waits.
type RetryCheckCancellation struct {
	Cancellation CheckCancellationRequested `json:"cancellation"`
	ReadOnly     bool                       `json:"read_only"`
	Attempt      int                        `json:"attempt"`
	At           time.Time                  `json:"at"`
}

func (RetryCheckCancellation) queueAction() {}

// Key separates each scheduled attempt from its completed predecessor.
func (a RetryCheckCancellation) Key() string {
	key := CancelChecks(a.Cancellation).Key()
	if a.ReadOnly {
		key = ObserveRetiredChecks(a.Cancellation).Key()
	}
	return fmt.Sprintf("%s/retry/%d", key, a.Attempt)
}

// Validate requires a bounded attempt number, wake time and exact targets.
func (a RetryCheckCancellation) Validate() error {
	if a.Attempt < 1 || a.Attempt >= CancellationAttemptLimit || a.At.IsZero() {
		return fmt.Errorf("invalid cancellation retry schedule")
	}
	return a.Cancellation.Validate()
}
func cancellationTarget(action Action) (CheckCancellationRequested, bool, int, bool) {
	switch a := action.(type) {
	case CancelChecks:
		return CheckCancellationRequested(a), false, 0, true
	case ObserveRetiredChecks:
		return CheckCancellationRequested(a), true, 0, true
	case RetryCheckCancellation:
		return a.Cancellation, a.ReadOnly, a.Attempt, true
	default:
		return CheckCancellationRequested{}, false, 0, false
	}
}

// CheckCancellationDeferred settles one unsuccessful attempt. Exhaustion is
// explicit historical evidence, never a queue block or landing observation.
type CheckCancellationDeferred struct {
	BatchID   string    `json:"batch_id"`
	TestedSHA string    `json:"tested_sha"`
	Key       string    `json:"key"`
	At        time.Time `json:"at"`
	Detail    string    `json:"detail"`
	Abandoned bool      `json:"abandoned"`
}

func (CheckCancellationDeferred) queueEvent() {}

// Validate requires the failed action identity and durable scheduling evidence.
func (e CheckCancellationDeferred) Validate() error {
	if e.At.IsZero() {
		return fmt.Errorf("cancellation deferral requires a time")
	}
	return requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA, "key", e.Key, "detail", e.Detail)
}

// DeferCheckCancellation is shared by workers and simulation for pending reads,
// timeouts, missing runs and other forge errors. Non-cancellation errors retain
// the caller's ordinary failure handling.
func DeferCheckCancellation(action Action, at time.Time, detail string) (CheckCancellationDeferred, bool) {
	a, _, attempt, ok := cancellationTarget(action)
	return CheckCancellationDeferred{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Key: action.Key(), At: at, Detail: detail, Abandoned: attempt+1 >= CancellationAttemptLimit}, ok
}
func (s *State) deferCancellation(e CheckCancellationDeferred) {
	if e.Validate() != nil || s.Batch(e.BatchID) != nil {
		return
	}
	for _, effect := range s.effects {
		a, readOnly, attempt, ok := cancellationTarget(effect)
		if !ok || effect.Key() != e.Key || a.BatchID != e.BatchID || a.TestedSHA != e.TestedSHA || e.Abandoned != (attempt+1 >= CancellationAttemptLimit) {
			continue
		}
		s.completeEffect(effect.Key())
		if !e.Abandoned {
			delay := min(30*time.Second*time.Duration(1<<attempt), 30*time.Minute)
			s.addEffect(RetryCheckCancellation{Cancellation: a, ReadOnly: readOnly, Attempt: attempt + 1, At: e.At.Add(delay)})
		}
		return
	}
}

// ObserveRetiredChecks follows unsupported cancellations through completion
// without attempting another mutation. It supplies history, never queue gates.
type ObserveRetiredChecks CheckCancellationRequested

func (ObserveRetiredChecks) queueAction() {}

// Key identifies the read-only follow-up for a retired batch.
func (a ObserveRetiredChecks) Key() string { return "batch/" + a.BatchID + "/cancel/observe" }

// Validate pins the same identities as the unsupported cancellation.
func (a ObserveRetiredChecks) Validate() error { return CheckCancellationRequested(a).Validate() }

// BatchRunsObserved records all accepted CI names independently of retry policy.
// It supplies cancellation targets and history, never success or failure gates.
type BatchRunsObserved struct {
	BatchID   string     `json:"batch_id"`
	TestedSHA string     `json:"tested_sha"`
	Runs      []CheckRun `json:"runs"`
}

func (BatchRunsObserved) queueEvent() {}

// Validate requires a batch commit and nonempty, distinct canonical run identities.
func (e BatchRunsObserved) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	return validateHistoricalRuns(e.Runs)
}

func validateHistoricalRuns(runs []CheckRun) error {
	if len(runs) == 0 {
		return fmt.Errorf("run observation requires identified runs")
	}
	seen := make(map[string]bool)
	for _, run := range runs {
		if run.ID == 0 {
			return fmt.Errorf("run observation requires native run IDs")
		}
		if err := validateCheckRuns([]CheckRun{run}); err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%s/%d/%d/%d", run.Source, run.Name, run.ID, run.WorkflowRunID, max(run.Attempt, 1))
		if seen[key] {
			return fmt.Errorf("duplicate observed run identity")
		}
		seen[key] = true
	}
	return nil
}
func (s *State) observeBatchRuns(e BatchRunsObserved) {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil || b == nil || b.Phase != Testing || b.InvalidationReason != "" || b.TestedSHA != e.TestedSHA {
		return
	}
	b.ObservedRuns = latestObservedChecks(b.ObservedRuns, e.Runs)
	b.LastObservedRuns = slices.Clone(e.Runs)
}

func (s *State) observeRetiredRuns(e ChecksObserved) {
	if e.Validate() != nil || s.Batch(e.BatchID) != nil {
		return
	}
	for i, effect := range s.effects {
		a, readOnly, _, ok := cancellationTarget(effect)
		if !ok || !readOnly || a.BatchID != e.BatchID || a.TestedSHA != e.TestedSHA {
			continue
		}
		a.Runs = slices.DeleteFunc(slices.Clone(a.Runs), func(run CheckRun) bool {
			return slices.ContainsFunc(e.Runs, func(observed CheckRun) bool { return observed.State != "pending" && sameCancellationRun(run, observed) })
		})
		if len(a.Runs) == 0 {
			s.completeEffect(effect.Key())
		} else if retry, yes := effect.(RetryCheckCancellation); yes {
			retry.Cancellation = a
			s.effects[i] = retry
		} else {
			s.effects[i] = ObserveRetiredChecks(a)
		}
		return
	}
}
