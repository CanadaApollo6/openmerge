package queue

import (
	"fmt"
	"slices"
	"time"
)

// FastForwardBatchSelected narrows a building batch to a consecutive linear
// prefix before any test ref is pushed. Remaining members keep queue order.
// Retrying an already selected prefix is harmless.
type FastForwardBatchSelected struct {
	BatchID string
	PRs     []BatchPR
}

// FastForwardRefBuilt records a test ref pointing at the unchanged final head.
// Unlike generated commits, the same SHA can be tested in several batches.
type FastForwardRefBuilt struct {
	BatchID, TestedSHA string
	At                 time.Time
}

// FastForwardBlocked rejects the first member when it cannot extend the base.
// Other members remain queued; OpenMerge never rebases a fast-forward head.
type FastForwardBlocked struct{ BatchID string }

func (FastForwardBatchSelected) queueEvent() {}
func (FastForwardRefBuilt) queueEvent()      {}
func (FastForwardBlocked) queueEvent()       {}

// Validate requires a batch and a nonempty, bounded list of distinct heads.
func (e FastForwardBatchSelected) Validate() error {
	if err := requireActionText("batch_id", e.BatchID); err != nil {
		return err
	}
	if len(e.PRs) > 128 {
		return fmt.Errorf("prs must contain at most 128 PRs")
	}
	return validateBatchPRs(e.PRs)
}

// Validate requires the pinned test commit and its check timer origin.
func (e FastForwardRefBuilt) Validate() error {
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA); err != nil {
		return err
	}
	if e.At.IsZero() {
		return fmt.Errorf("at must not be zero")
	}
	return nil
}

// Validate requires the building batch whose first head was rejected.
func (e FastForwardBlocked) Validate() error { return requireActionText("batch_id", e.BatchID) }

type wireFastForwardBatchSelected struct {
	BatchID string        `json:"batch_id"`
	PRs     []wireBatchPR `json:"prs"`
}
type wireFastForwardRefBuilt struct {
	BatchID   string    `json:"batch_id"`
	TestedSHA string    `json:"tested_sha"`
	At        time.Time `json:"at"`
}
type wireFastForwardBlocked struct {
	BatchID string `json:"batch_id"`
}

func (s *State) selectFastForward(e FastForwardBatchSelected) {
	if e.Validate() != nil || !s.batchIn(e.BatchID, Building) || len(e.PRs) > len(s.Active.PRs) || !slices.Equal(e.PRs, s.Active.PRs[:len(e.PRs)]) {
		return
	}
	if len(e.PRs) < len(s.Active.PRs) {
		rest := pendingGroup{prs: slices.Clone(s.Active.PRs[len(e.PRs):]), bisection: s.Active.FromBisection}
		s.pending = append([]pendingGroup{rest}, s.pending...)
	}
	s.Active.PRs = slices.Clone(e.PRs)
	s.Active.Method = FastForward
}

func dequeueDetail(reason DequeueReason) string {
	if reason == FastForwardRequired {
		return "Head cannot fast-forward onto the current base; update the branch and queue it again"
	}
	return string(reason)
}
