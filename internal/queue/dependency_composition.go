package queue

import (
	"errors"
	"slices"
)

var errDependencyComposition = errors.New("invalid dependency composition targets")

// DependencyCompositionsInvalidated is the additive, exact companion to a
// discarded dependency composition and its ADR 0009 suffix. It leaves Entries
// in original order and uses the existing discard/cancellation machinery.
type DependencyCompositionsInvalidated struct {
	BatchIDs       []string `json:"batch_ids"`
	CompositionIDs []uint64 `json:"composition_ids"`
	Reason         string   `json:"reason"`
}

func (DependencyCompositionsInvalidated) queueEvent() {}
// Validate requires the exact bounded suffix and one immutable companion per batch.
func (e DependencyCompositionsInvalidated) Validate() error {
	if err := (InvalidateBatches{BatchIDs: e.BatchIDs, Reason: e.Reason}).Validate(); err != nil {
		return err
	}
	if e.Reason != "dependency-changed" || len(e.CompositionIDs) != len(e.BatchIDs) {
		return errDependencyComposition
	}
	for i, id := range e.CompositionIDs {
		if id == 0 || slices.Contains(e.CompositionIDs[:i], id) {
			return errDependencyComposition
		}
	}
	return nil
}

// WithDependencyInvalidation marks a suffix on an immutable planning/fold view.
// Landing is protected by invalidateFrom: its historical decision always settles.
// The shared reader calls this when recorded side facts invalidate a companion,
// so replay and the planner derive the same durable, latched reason.
func (s State) WithDependencyInvalidation(batch string) State {
	if b := s.Batch(batch); b != nil && b.Phase != Landing {
		s = cloneState(s)
		s.invalidateFrom(s.Batch(batch), "dependency-changed")
	}
	return s
}
func (s *State) invalidateDependencyCompositions(e DependencyCompositionsInvalidated) {
	if e.Validate() != nil {
		return
	}
	// A raw queue-only fold still accepts the exact event emitted by the store;
	// the composed reader additionally verifies every immutable companion ID.
	suffix := []string{}
	found := false
	for _, b := range s.Batches() {
		found = found || b.ID == e.BatchIDs[0]
		if found {
			suffix = append(suffix, b.ID)
		}
	}
	if !slices.Equal(suffix, e.BatchIDs) {
		return
	}
	for _, id := range e.BatchIDs {
		b := s.Batch(id)
		if b == nil || b.Phase == Landing || b.InvalidationReason != "dependency-changed" && b.InvalidationReason != "" {
			return
		}
	}
	for _, id := range e.BatchIDs {
		s.Batch(id).InvalidationReason = e.Reason
	}
	s.applyInvalidation(BatchesInvalidated{BatchIDs: e.BatchIDs, Reason: e.Reason})
}
