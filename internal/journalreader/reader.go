// Package journalreader composes the strict queue, dependency and provenance
// readers. The store owns the unwired transactional append adapter; this package
// validates supplied records and encodes only derived audit cursor state.
package journalreader

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// ErrReference identifies missing, out-of-order or mismatched journal evidence.
var ErrReference = errors.New("unresolved or mismatched journal evidence reference")

// Lookup reads an exact position in this queue's immutable journal, including
// positions before a snapshot. It must not fetch mutable external observations.
type Lookup func(uint64) ([]byte, error)

// Event preserves the separate sealed event interfaces. Queue is nil for new
// evidence; callers must not pass nil through the queue planner's fold.
type Event struct {
	Queue      queue.Event
	Dependency dependency.Event
	Provenance pj.Event
}

// DecodeEvent dispatches by kind, then uses the owning strict decoder. A failed
// new-kind decode never falls back to a permissive or opaque queue event.
func DecodeEvent(raw []byte) (Event, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return Event{}, err
	}
	var e Event
	var err error
	switch header.Type {
	case "change_provenance_observed", "landing_provenance_recorded":
		e.Provenance, err = pj.DecodeEvent(raw)
	case "dependency_journal_initialized", "change_dependencies_declared", "dependency_policy_applied", "dependency_declaration_acknowledged", "dependency_declaration_self_acknowledged", "dependency_observed", "dependency_decision_recorded", "dependency_composition_recorded":
		e.Dependency, err = dependency.DecodeEvent(raw)
	default:
		e.Queue, err = queue.DecodeEvent(raw)
	}
	return e, err
}

// QueueEvent is for evidence-only scans. Authorization readers use Reader.Apply
// to validate references and folds as well. New evidence has no queue effect.
func QueueEvent(raw []byte) (queue.Event, error) {
	e, err := DecodeEvent(raw)
	return e.Queue, err
}

// Reader holds a composed replay, with no I/O other than a supplied exact-record
// lookup. Missing external records fail closed, including across snapshot cuts.
// An absent independently verified dependency binding quarantines that projection:
// it can be folded for compatibility, but Dependencies never exposes it as valid.
type Reader struct {
	Queue        queue.State
	Provenance   pj.State
	dependencies dependency.State
	expected     dependency.JournalInitialized
	lookup       Lookup
	changeID     ChangeIdentity
	lastID       uint64
	composed     bool // Persisted by the cursor snapshot format, never its envelope.
}

// New creates a reader over an immutable queue seed and optional trusted binding.
func New(seed queue.State, expected dependency.JournalInitialized, lookup Lookup) Reader {
	return Reader{Queue: seed, expected: expected, lookup: lookup}
}

// Dependencies refuses to expose unverified namespace/incarnation evidence as a
// satisfied dependency. Writers must supply independently verified startup facts.
func (r Reader) Dependencies() (dependency.State, error) {
	if err := r.dependencies.VerifyBinding(r.expected); err != nil {
		return dependency.State{}, err
	}
	return r.dependencies, nil
}

// Apply is atomic on validation failure. Queue bytes and decisions are unchanged
// by side evidence. IDs are global journal positions, not side-fold counters.
func (r *Reader) Apply(id uint64, raw []byte) (_ Event, applyErr error) {
	defer func() {
		if applyErr != nil {
			applyErr = fmt.Errorf("event %d: %w", id, applyErr)
		}
	}()
	e, err := DecodeEvent(raw)
	if err != nil {
		return Event{}, err
	}
	if id == 0 || id <= r.lastID {
		return Event{}, ErrReference
	}
	next := *r
	switch {
	case e.Queue != nil:
		if invalid, ok := e.Queue.(queue.DependencyCompositionsInvalidated); ok {
			pending := r.Queue.DependencyInvalidationTargets()
			if !slices.Equal(pending, invalid.BatchIDs) {
				return Event{}, ErrReference
			}
			for i, batch := range invalid.BatchIDs {
				_, ref, ok := r.dependencies.Composition(batch)
				if !ok || ref != invalid.CompositionIDs[i] || r.dependencies.CompositionCause(batch) != invalid.CauseEventIDs[i] {
					return Event{}, ErrReference
				}
			}
		}
		next.Queue = queue.Apply(r.Queue, e.Queue)
		if invalid, ok := e.Queue.(queue.DependencyCompositionsInvalidated); ok {
			for _, batch := range invalid.BatchIDs {
				if next.Queue.Batch(batch) != nil {
					return Event{}, ErrReference
				}
			}
		}
		next.Provenance = retireTerminal(r.Provenance, id, e.Queue, r.Queue, next.Queue)
	case e.Dependency != nil:
		if c, ok := e.Dependency.(dependency.CompositionRecorded); ok {
			if err := r.validateDependencyComposition(id, c); err != nil {
				return Event{}, err
			}
		}
		if d, ok := e.Dependency.(dependency.DecisionRecorded); ok {
			if err := r.validateDependencyDecision(id, d); err != nil {
				return Event{}, err
			}
		}
		if init, ok := e.Dependency.(dependency.JournalInitialized); ok && r.expected != (dependency.JournalInitialized{}) {
			if err := init.VerifyBinding(r.expected); err != nil {
				return Event{}, err
			}
		}
		next.dependencies, err = dependency.Apply(r.dependencies, id, e.Dependency)
	case e.Provenance != nil:
		// Validate external targets before transferring the owned fold. An invalid
		// target cannot mutate lifetime fences. Oversized companions fail pre-lookup.
		if l, ok := e.Provenance.(pj.Landing); ok {
			if pj.ReferenceCount(l) > pj.MaxLandingReferences {
				return Event{}, pj.ErrLimit
			}
			if err := r.validateLanding(id, l); err != nil {
				return Event{}, err
			}
		}
		next.Provenance, err = pj.ApplyOwned(r.Provenance, id, e.Provenance)

	}
	if err != nil {
		return Event{}, err
	}
	// Side facts latch invalidation immediately, including if later observations
	// restore eligibility before a worker next polls. A landing is never retired.
	if next.dependencies.HasCompositions() {
		batches := []string{}
		for _, b := range next.Queue.Batches() {
			batches = append(batches, b.ID)
			if e.Dependency != nil {
				if c, _, ok := next.dependencies.Composition(b.ID); ok && !next.dependencies.CompositionValid(c) {
					next.Queue = next.Queue.WithDependencyInvalidation(b.ID)
				}
			}
		}
		for _, b := range next.Queue.Batches() {
			if e.Dependency != nil && b.InvalidationReason == "dependency-changed" {
				next.dependencies = next.dependencies.LatchCompositionCause(b.ID)
			}
		}
		next.dependencies = next.dependencies.RetainCompositions(batches)
	}
	next.composed = next.composed || e.Queue == nil
	next.lastID = id
	*r = next
	return e, nil
}

func (r Reader) target(ref, id uint64) (Event, error) {
	if ref == 0 || ref >= id || r.lookup == nil {
		return Event{}, ErrReference
	}
	raw, err := r.lookup(ref)
	if err != nil {
		return Event{}, fmt.Errorf("%w: position %d: %v", ErrReference, ref, err)
	}
	e, err := DecodeEvent(raw)
	if err != nil {
		return Event{}, fmt.Errorf("%w: position %d: %v", ErrReference, ref, err)
	}
	return e, nil
}

func matches(b queue.ApprovalBasis, req p.InventoryRequest) bool {
	return string(b.ChangeID) == req.ChangeID && strconv.FormatUint(uint64(b.Revision), 10) == req.Revision && b.Head == req.Head
}

func (r Reader) validateLanding(id uint64, l pj.Landing) error {
	target, err := r.target(l.DecisionID, id)
	if err != nil {
		return err
	}
	d, ok := target.Queue.(queue.LandingDecisionRecorded)
	if !ok || d.BatchID != l.BatchID || d.AttemptID != l.AttemptID {
		return ErrReference
	}
	// DecisionID anchors the attempt. Each member must additionally resolve an
	// exact per-member decision, either that anchor or one of its layer references.
	// This composes the existing contracts without adding fields to either event.
	anchorMatched := false
	targets := map[uint64]Event{l.DecisionID: target}
	resolve := func(ref uint64) (Event, error) {
		if e, ok := targets[ref]; ok {
			return e, nil
		}
		e, err := r.target(ref, id)
		if err == nil {
			targets[ref] = e
		}
		return e, err
	}
	for _, m := range l.Members {
		memberBound := decisionMatches(d, l, m.Request)
		anchorMatched = anchorMatched || memberBound
		for _, layer := range m.Layers {
			for _, ref := range layer.References {
				e, err := resolve(ref)
				if err != nil {
					return err
				}
				valid := false
				switch v := e.Queue.(type) {
				case queue.ApprovalEligibilityObserved:
					valid = matches(v.Basis, m.Request)
				case queue.ReviewStanceObserved:
					valid = matches(v.Basis, m.Request)
				case queue.ChangeAccountabilityObserved:
					valid = matches(v.Basis, m.Request)
				case queue.ChangeIntentObserved:
					valid = matches(v.Basis, m.Request)
				case queue.ApprovalReceiptRecorded:
					valid = v.Accepted && matches(v.Basis, m.Request)
				case queue.ChangeClassified:
					inputs := v.Evidence.Classification.Inputs
					valid = inputs.ChangeID == m.Request.ChangeID && strconv.FormatUint(inputs.Revision, 10) == m.Request.Revision && inputs.Head == m.Request.Head
				case queue.ChecksCompleted:
					valid = v.TestedSHA == d.TestedSHA
				case queue.CheckProvenanceObserved:
					valid = v.BatchID == l.BatchID && v.TestedSHA == d.TestedSHA
				case queue.LandingDecisionRecorded:
					// Variant and its approval/remedy payload are per member. The
					// batch/attempt and final tested/landed identities are shared;
					// Mapping.Build.Result may be an intermediate member commit.
					valid = decisionMatches(v, l, m.Request) && v.TestedSHA == d.TestedSHA && v.LandedSHA == d.LandedSHA
					memberBound = memberBound || valid
				}
				if v, ok := e.Provenance.(pj.Observation); ok {
					valid = slices.Contains(m.ObservationIDs, ref) && reflect.DeepEqual(v.Request, m.Request)
				}
				// Dependency observations require a verified incarnation and a matching
				// consumer. Unverified evidence is never allowed to certify a true layer.
				if v, ok := e.Dependency.(dependency.Observed); ok {
					_, bound := r.Dependencies()
					valid = bound == nil && v.Namespace == r.expected.Namespace && v.ChangeID == m.Request.ChangeID && strconv.FormatUint(v.Revision, 10) == m.Request.Revision
				}
				if !valid {
					return ErrReference
				}
			}
		}
		if !memberBound {
			return ErrReference
		}
	}
	if !anchorMatched {
		return ErrReference
	}
	return nil
}

func decisionMatches(d queue.LandingDecisionRecorded, l pj.Landing, req p.InventoryRequest) bool {
	if d.BatchID != l.BatchID || d.AttemptID != l.AttemptID {
		return false
	}
	if d.Approval != nil {
		return matches(d.Approval.Basis, req)
	}
	if d.Remedy != nil {
		return string(d.Remedy.ChangeID) == req.ChangeID && strconv.FormatUint(uint64(d.Remedy.Revision), 10) == req.Revision && d.Remedy.Head == req.Head
	}
	return false
}

// WithLookup replaces the immutable lookup with a transaction-local candidate overlay.
func (r *Reader) WithLookup(lookup Lookup) { r.lookup = lookup }

// ChangeIdentity derives a PR's ADR 0007 ChangeID from the journal's stored
// canonical queue key, never from a discovery alias or a companion's claim.
type ChangeIdentity func(pr int) (queue.ChangeID, error)

// WithChangeIdentity installs the canonical deriver. Without it, dependency
// decision companions fail closed: their consumer member cannot be bound.
func (r *Reader) WithChangeIdentity(derive ChangeIdentity) { r.changeID = derive }

// VerifyDependencyBinding checks a writer-supplied independent identity without
// changing the reader's trust configuration or exposing satisfaction.
func (r Reader) VerifyDependencyBinding(binding dependency.JournalInitialized) error {
	return r.dependencies.VerifyBinding(binding)
}

// BoundDependencies verifies a writer-supplied, independently verified binding
// and returns the projection for admission. It never trusts the snapshot's own
// initialization, and does not change the reader's quarantine for other callers.
func (r Reader) BoundDependencies(binding dependency.JournalInitialized) (dependency.State, error) {
	if err := r.dependencies.VerifyBinding(binding); err != nil {
		return dependency.State{}, err
	}
	return r.dependencies, nil
}

// validateDependencyDecision binds a member companion to its exact durable start
// in this queue. Observations must be recorded after that start, inside the same
// start transaction; a decision, when the attempt has one, must be this member's.
// Source settlements live in other queues and are revalidated by the store under
// their queue locks; this reader cannot and does not dereference them.
func (r Reader) validateDependencyDecision(id uint64, d dependency.DecisionRecorded) error {
	start, err := r.target(d.StartEventID, id)
	if err != nil {
		return err
	}
	s, ok := start.Queue.(queue.LandingStarted)
	b := r.Queue.Batch(d.BatchID)
	if !ok || s.BatchID != d.BatchID || s.AttemptID != d.AttemptID || b == nil || b.Phase != queue.Landing || b.LandingAttempt.ID != d.AttemptID || b.TestedSHA != d.TestedSHA || b.LandingCommit() != d.LandedSHA || uint64(len(b.PRs)) != d.Members {
		return ErrReference
	}
	pin := b.PRs[d.Member]
	bindings := r.Queue.BatchRevisions(d.BatchID)
	revision, known := r.Queue.PRRevision(pin.PR)
	if r.changeID == nil || d.Alias.Number != uint64(pin.PR) {
		return ErrReference
	}
	// Legacy revision observations carry no ChangeID: bind the member to the
	// stored canonical queue key's derivation, exactly as the store did under the
	// start lock, and to any recorded ChangeID as well. Never accept another.
	derived, err := r.changeID(pin.PR)
	if err != nil || string(derived) != d.ChangeID || !known || len(bindings) != len(b.PRs) || revision.ChangeID != "" && revision.ChangeID != derived || uint64(bindings[d.Member].Revision) != d.Revision || pin.HeadSHA != d.Head || bindings[d.Member].HeadSHA != d.Head {
		return ErrReference
	}
	req := p.InventoryRequest{ChangeID: d.ChangeID, Revision: strconv.FormatUint(d.Revision, 10), Head: d.Head}
	var expected *queue.LandingDecisionRecorded
	for _, decision := range r.Queue.LandingDecisions() {
		if decision.BatchID == d.BatchID && decision.AttemptID == d.AttemptID && decisionMatches(decision, pj.Landing{BatchID: d.BatchID, AttemptID: d.AttemptID}, req) {
			expected = &decision
		}
	}
	if (expected == nil) != (d.DecisionID == 0) {
		return ErrReference
	}
	if expected != nil {
		target, err := r.target(d.DecisionID, id)
		if err != nil {
			return err
		}
		decision, ok := target.Queue.(queue.LandingDecisionRecorded)
		if !ok || !reflect.DeepEqual(decision, *expected) || decision.TestedSHA != d.TestedSHA || decision.LandedSHA != d.LandedSHA {
			return ErrReference
		}
	}
	for _, observation := range d.Admission.ObservationIDs {
		if observation <= d.StartEventID || observation >= id {
			return ErrReference
		}
	}
	return nil
}

func (r Reader) validateDependencyComposition(id uint64, c dependency.CompositionRecorded) error {
	target, err := r.target(c.StartEventID, id)
	if err != nil {
		return err
	}
	b := r.Queue.Batch(c.BatchID)
	if b == nil || b.Phase != queue.Building || b.BaseSHA != c.BaseSHA || len(b.PRs) != len(c.Members) {
		return ErrReference
	}
	switch start := target.Queue.(type) {
	case queue.BatchStarted:
		if start.BatchID != b.ID || start.BaseSHA != b.BaseSHA || !slices.Equal(start.PRs, b.PRs) || b.Speculative {
			return ErrReference
		}
	case queue.ChainedBatchStarted:
		if start.BatchID != b.ID || start.BaseSHA != b.BaseSHA || !slices.Equal(start.PRs, b.PRs) || !b.Speculative || start.Position != b.ChainPosition || start.ParentBatchID != b.ParentBatchID {
			return ErrReference
		}
	default:
		return ErrReference
	}
	bindings := r.Queue.BatchRevisions(b.ID)
	for i, m := range c.Members {
		if r.changeID == nil || len(bindings) != len(b.PRs) {
			return ErrReference
		}
		change, err := r.changeID(b.PRs[i].PR)
		if err != nil || string(change) != m.ChangeID || m.Alias.Number != uint64(b.PRs[i].PR) || m.Head != b.PRs[i].HeadSHA || m.Revision != uint64(bindings[i].Revision) {
			return ErrReference
		}
	}
	return nil
}
