package queue

import (
	"fmt"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// FastForwardBatchSelected narrows a building batch to a consecutive linear
// prefix before any test ref is pushed. Remaining members keep queue order.
// Retrying an already selected prefix is harmless.
type FastForwardBatchSelected struct {
	BatchID string
	PRs     []BatchPR
}

// FastForwardRefBuilt records a unique empty marker above the pinned final head.
type FastForwardRefBuilt struct {
	BatchID, TestedSHA, HeadSHA string
	At                          time.Time
}

// FastForwardBlocked rejects the first member when it cannot extend the base.
// Other members remain queued; OpenMerge never rebases a fast-forward head.
type FastForwardBlocked struct{ BatchID string }

// FastForwardMergeCommitsBlocked rejects a selected head whose new history
// contains merges while the base requires linear history.
type FastForwardMergeCommitsBlocked struct {
	BatchID string
	PR      int
}

func (FastForwardBatchSelected) queueEvent()       {}
func (FastForwardRefBuilt) queueEvent()            {}
func (FastForwardBlocked) queueEvent()             {}
func (FastForwardMergeCommitsBlocked) queueEvent() {}

// Validate pins the rejection to one member of a building batch.
func (e FastForwardMergeCommitsBlocked) Validate() error {
	if err := requireActionText("batch_id", e.BatchID); err != nil {
		return err
	}
	if e.PR <= 0 {
		return fmt.Errorf("pr must be positive")
	}
	return nil
}

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
	if err := requireActionText("batch_id", e.BatchID, "tested_sha", e.TestedSHA, "head_sha", e.HeadSHA); err != nil {
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
	HeadSHA   string    `json:"head_sha"`
	At        time.Time `json:"at"`
}
type wireFastForwardBlocked struct {
	BatchID string `json:"batch_id"`
}

type wireFastForwardMergeCommitsBlocked struct {
	BatchID string `json:"batch_id"`
	PR      int    `json:"pr"`
}

func (s *State) selectFastForward(e FastForwardBatchSelected) {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil || !s.batchIn(e.BatchID, Building) || len(e.PRs) > len(b.PRs) || !slices.Equal(e.PRs, b.PRs[:len(e.PRs)]) {
		return
	}
	if len(e.PRs) < len(b.PRs) {
		rest := pendingGroup{prs: slices.Clone(b.PRs[len(e.PRs):]), bisection: b.FromBisection}
		s.pending = append([]pendingGroup{rest}, s.pending...)
	}
	b.PRs = slices.Clone(e.PRs)
	b.Method = FastForward
}

func dequeueDetail(reason DequeueReason) string {
	if reason == RebaseCompositionExceeded {
		return fmt.Sprintf("rebase composition exceeds %d commits; use squash or merge, or split the change", forge.MaxFirstParentCommits)
	}
	if reason == MergeCommitsNotAllowed {
		return "Base requires linear history; remove merge commits from the branch and queue it again"
	}
	if reason == FastForwardRequired {
		return "Head cannot fast-forward onto the current base; update the branch and queue it again"
	}
	return string(reason)
}

// FastForwardResolveLanding persists the head landed by a marker-tested attempt.
// ResolveLanding retains its original wire shape and exact-tested-SHA behavior.
type FastForwardResolveLanding struct {
	ResolveLanding
	HeadSHA string
}

func (FastForwardResolveLanding) queueAction() {}

// Key pins recovery to both the landing attempt and its head.
func (a FastForwardResolveLanding) Key() string { return a.ResolveLanding.Key() + "/head/" + a.HeadSHA }

// Validate requires a pinned head distinct from the tested marker.
func (a FastForwardResolveLanding) Validate() error {
	if err := a.ResolveLanding.Validate(); err != nil {
		return err
	}
	if a.HeadSHA == a.TestedSHA {
		return fmt.Errorf("head_sha must differ from tested_sha")
	}
	return requireActionText("head_sha", a.HeadSHA)
}

// IsLandingResolution identifies both durable recovery action forms.
func IsLandingResolution(a Action) bool {
	switch a.(type) {
	case ResolveLanding, FastForwardResolveLanding:
		return true
	}
	return false
}

type wireFastForwardResolveLanding struct {
	BatchID    string `json:"batch_id"`
	AttemptID  string `json:"attempt_id"`
	TestedSHA  string `json:"tested_sha"`
	FenceToken string `json:"fence_token"`
	HeadSHA    string `json:"head_sha"`
}
