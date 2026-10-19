package queue

import (
	"fmt"
	"slices"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// RebaseCompositionObserved bounds the pinned head ^ build-base composition
// before CI. Counts above Limit are saturated at Limit+1.
type RebaseCompositionObserved struct {
	BatchID string    `json:"batch_id"`
	PRs     []BatchPR `json:"prs"`
	Counts  []int     `json:"counts"`
	Limit   int       `json:"limit"`
}

func (RebaseCompositionObserved) queueEvent() {}
func (e RebaseCompositionObserved) Validate() error {
	if e.BatchID == "" || len(e.PRs) == 0 || len(e.PRs) != len(e.Counts) || e.Limit < 1 || e.Limit > forge.MaxFirstParentCommits || len(e.PRs) > 128 {
		return fmt.Errorf("invalid rebase composition")
	}
	seen := map[int]bool{}
	for i, p := range e.PRs {
		if p.PR < 1 || p.HeadSHA == "" || seen[p.PR] || e.Counts[i] < 1 || e.Counts[i] > e.Limit+1 {
			return fmt.Errorf("invalid rebase composition member")
		}
		seen[p.PR] = true
	}
	return nil
}

// Select rejects unsupported individual changes and defers the ordered suffix
// once the aggregate budget is full. Neither outcome is a CI/build failure.
func (e RebaseCompositionObserved) Select() (selected, rejected, deferred []BatchPR) {
	total, full := 0, false
	for i, p := range e.PRs {
		n := e.Counts[i]
		if n > e.Limit {
			rejected = append(rejected, p)
			continue
		}
		if full || n > e.Limit-total {
			full = true
			deferred = append(deferred, p)
			continue
		}
		selected = append(selected, p)
		total += n
	}
	return
}
func (s *State) applyRebaseComposition(e RebaseCompositionObserved) {
	b := s.Batch(e.BatchID)
	if e.Validate() != nil || !s.batchIn(e.BatchID, Building) || !slices.Equal(b.PRs, e.PRs) {
		return
	}
	selected, rejected, deferred := e.Select()
	// Detach rejected/deferred members before requesting dequeues so a rejected
	// member cannot invalidate the supported speculative batch beside it.
	b.PRs = slices.Clone(selected)
	for _, p := range rejected {
		s.requestDequeue(p.PR, RebaseCompositionExceeded)
	}
	if len(deferred) > 0 {
		s.pending = append([]pendingGroup{{prs: slices.Clone(deferred), bisection: b.FromBisection}}, s.pending...)
	}
	if len(selected) == 0 {
		s.discardBatch(b)
	}
}
