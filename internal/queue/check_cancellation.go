package queue

import (
	"fmt"
	"slices"
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
	return a.ID == b.ID && a.Attempt == b.Attempt && a.Source == b.Source && a.Name == b.Name && a.WorkflowRunID == b.WorkflowRunID
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
	for _, effect := range s.effects {
		a, ok := effect.(CancelChecks)
		if !ok || a.BatchID != e.BatchID || a.TestedSHA != e.TestedSHA {
			continue
		}
		for _, run := range e.Confirmed {
			if !slices.ContainsFunc(a.Runs, func(target CheckRun) bool { return sameCancellationRun(target, run) }) {
				return
			}
		}
		if e.Unsupported || len(a.Runs) == len(e.Confirmed) {
			s.completeEffect(a.Key())
			if e.Unsupported {
				pending := slices.DeleteFunc(slices.Clone(a.Runs), func(r CheckRun) bool {
					return r.State != "pending" || slices.ContainsFunc(e.Confirmed, func(c CheckRun) bool { return sameCancellationRun(r, c) })
				})
				if len(pending) > 0 {
					a.Runs = pending
					s.addEffect(ObserveRetiredChecks(a))
				}
			}
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

// Validate requires a batch commit and normalized, distinct producer names.
func (e BatchRunsObserved) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	return validateCheckRuns(e.Runs)
}
func (s *State) observeBatchRuns(e BatchRunsObserved) {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil || b == nil || b.Phase != Testing || b.InvalidationReason != "" || b.TestedSHA != e.TestedSHA {
		return
	}
	b.ObservedRuns = latestObservedChecks(b.ObservedRuns, e.Runs)
}
