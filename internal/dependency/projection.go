package dependency

import (
	"fmt"
	"maps"
	"slices"
)

const maxProjectionEntries = 4096

type declarationProjection struct {
	EventID uint64               `json:"event_id"`
	Event   DependenciesDeclared `json:"event"`
	Held    bool                 `json:"held"`
	Unknown bool                 `json:"unknown"`
	Editors []string             `json:"editors"`
	Ack     *ackProjection       `json:"ack"`
}
type ackProjection struct {
	EventID    uint64        `json:"event_id"`
	Kind       string        `json:"kind"`
	Event      Acknowledged  `json:"event"`
	Governing  PolicyApplied `json:"governing"`
	Overridden []string      `json:"overridden"`
	Revoked    bool          `json:"revoked"`
}
type policyProjection struct {
	EventID uint64        `json:"event_id"`
	Event   PolicyApplied `json:"event"`
}
type observationProjection struct {
	EventID uint64   `json:"event_id"`
	Event   Observed `json:"event"`
}

// State holds only current side facts. Historical evidence remains in the journal;
// no SQL tables, queue indexes or runtime state are modified. Values are immutable
// through Apply and all accessors, including slices supplied in events.
type State struct {
	initialization *JournalInitialized
	lastID         uint64
	lastHash       string
	declarations   map[string]declarationProjection
	policies       map[string]policyProjection
	observations   map[string]observationProjection
}

func cloneDeclaration(d declarationProjection) declarationProjection {
	d.Event.Hold.Editors = slices.Clone(d.Event.Hold.Editors)
	d.Event.Edges = slices.Clone(d.Event.Edges)
	d.Event.Bindings = slices.Clone(d.Event.Bindings)
	d.Event.Attribution.Editors = slices.Clone(d.Event.Attribution.Editors)
	d.Event.Accountability.Humans = slices.Clone(d.Event.Accountability.Humans)
	d.Editors = slices.Clone(d.Editors)
	if d.Ack != nil {
		a := *d.Ack
		a.Event.Exclusions = slices.Clone(a.Event.Exclusions)
		a.Governing = clonePolicy(a.Governing)
		a.Overridden = slices.Clone(a.Overridden)
		d.Ack = &a
	}
	return d
}
func clonePolicy(p PolicyApplied) PolicyApplied {
	p.Policy.AllowFrom = slices.Clone(p.Policy.AllowFrom)
	p.Policy.AllowTo = slices.Clone(p.Policy.AllowTo)
	p.Writers = slices.Clone(p.Writers)
	return p
}
func (s State) clone() State {
	if s.initialization != nil {
		e := *s.initialization
		s.initialization = &e
	}
	s.declarations = maps.Clone(s.declarations)
	if s.declarations == nil {
		s.declarations = map[string]declarationProjection{}
	}
	s.policies = maps.Clone(s.policies)
	if s.policies == nil {
		s.policies = map[string]policyProjection{}
	}
	s.observations = maps.Clone(s.observations)
	if s.observations == nil {
		s.observations = map[string]observationProjection{}
	}
	return s
}

// Prior returns the current alias-scoped fence, including a final-edge removal.
func (s State) Prior(alias SourceAlias) Basis {
	d, ok := s.declarations[alias.key()]
	if !ok {
		return Basis{}
	}
	return Basis{d.EventID, d.Event.Digest}
}

// Declaration returns detached current alias-scoped declaration evidence.
func (s State) Declaration(alias SourceAlias) (DependenciesDeclared, bool) {
	d, ok := s.declarations[alias.key()]
	return cloneDeclaration(d).Event, ok
}

// Observation returns the current side fact and its journal fence for this key.
func (s State) Observation(read Observed) (Observed, uint64, bool) {
	o, ok := s.observations[read.key()]
	return o.Event, o.EventID, ok
}
func (s State) policy(d DependenciesDeclared) policyProjection {
	return s.policies[(PolicyApplied{Alias: d.Alias, Queue: d.Queue}).key()]
}

// HoldStatus is an authorized-only projection, never a landing/admission decision.
// MultiWriterWarning counts authenticated writers BEFORE change exclusions.
type HoldStatus struct {
	Held                    bool
	UnknownEditor           bool
	ExcludedEditors         []string
	LastAcknowledgementKind string
	AcknowledgementRevoked  bool
	MultiWriterWarning      bool
}

// Hold returns detached authorized-only hold, acknowledgement and warning status.
func (s State) Hold(alias SourceAlias) HoldStatus {
	d, ok := s.declarations[alias.key()]
	if !ok {
		return HoldStatus{}
	}
	p := s.policy(d.Event)
	h := HoldStatus{Held: d.Held, UnknownEditor: d.Unknown, ExcludedEditors: slices.Clone(d.Editors), MultiWriterWarning: p.Event.selfAckEnabled() && len(p.Event.Writers) > 1}
	if d.Ack != nil {
		h.LastAcknowledgementKind = d.Ack.Kind
		h.AcknowledgementRevoked = d.Ack.Revoked
	}
	return h
}

// Apply consumes an existing journal ID (gaps are allowed). An exact last-record
// retry is idempotent. Older/out-of-order records and changed duplicate IDs fail
// closed instead of keeping an unbounded dedupe history in the snapshot. Store
// dedupe keys and ordered replay remain the persistence boundary in later slices.
func Apply(s State, id uint64, event Event) (State, error) {
	_, raw, err := EncodeEvent(event)
	if err != nil {
		return s, err
	}
	hash := digest("openmerge.dependency.record", string(raw))
	if id == 0 {
		return s, ErrMalformed
	}
	if id == s.lastID && hash == s.lastHash {
		return s, nil
	}
	if id <= s.lastID {
		return s, ErrStale
	}
	next := s.clone()
	if e, ok := event.(JournalInitialized); ok {
		if s.initialization != nil {
			return s, fmt.Errorf("journal already initialized: rotate through stopped recovery, never replay a new incarnation over old facts")
		}
		next.initialization = &e
	} else {
		if s.initialization == nil {
			return s, ErrOutOfScope
		}
		if err := next.apply(id, event); err != nil {
			return s, err
		}
	}
	if len(next.declarations) > maxProjectionEntries || len(next.policies) > maxProjectionEntries || len(next.observations) > maxProjectionEntries*MaxDirectEdges {
		return s, ErrLimit
	}
	next.lastID = id
	next.lastHash = hash
	return next, nil
}
func (s *State) apply(id uint64, event Event) error {
	var alias SourceAlias
	var namespace string
	switch e := event.(type) {
	case DependenciesDeclared:
		alias = e.Alias
		namespace = e.Namespace
	case PolicyApplied:
		alias = e.Alias
		namespace = e.Namespace
	case Acknowledged:
		alias = e.Alias
		namespace = e.Namespace
	case SelfAcknowledged:
		alias = e.Acknowledged.Alias
		namespace = e.Acknowledged.Namespace
	case Observed:
		alias = e.Alias
		namespace = e.Namespace
	default:
		return ErrMalformed
	}
	if namespace != s.initialization.Namespace {
		return ErrOutOfScope
	}
	if err := alias.validate(s.initialization.scope()); err != nil {
		return err
	}
	switch e := event.(type) {
	case DependenciesDeclared:
		return s.declare(id, e)
	case PolicyApplied:
		prior := s.policies[e.key()]
		if e.PriorEventID != prior.EventID {
			return ErrStale
		}
		s.policies[e.key()] = policyProjection{id, clonePolicy(e)}
		// Retain the audited self-ack. Once revoked, re-enabling cannot resurrect it.
		if !e.selfAckEnabled() {
			for k, d := range s.declarations {
				if (PolicyApplied{Alias: d.Event.Alias, Queue: d.Event.Queue}).key() != e.key() || d.Ack == nil || d.Ack.Kind != "dependency_declaration_self_acknowledged" {
					continue
				}
				d = cloneDeclaration(d)
				d.Held = true
				d.Ack.Revoked = true
				s.declarations[k] = d
			}
		}
	case Acknowledged:
		return s.acknowledge(id, e, false, nil)
	case SelfAcknowledged:
		return s.acknowledge(id, e.Acknowledged, true, e.Overridden)
	case Observed:
		d, ok := s.declarations[e.Alias.key()]
		if !ok || e.Declaration != s.Prior(e.Alias) || e.ChangeID != d.Event.ChangeID || e.Revision != d.Event.Revision {
			return ErrStale
		}
		if !slices.ContainsFunc(d.Event.Edges, func(edge DeclaredEdge) bool { return edge.Reference == e.Source }) {
			return ErrOutOfScope
		}
		prior := s.observations[e.key()]
		if e.ExpectedPrior != prior.EventID || e.ReadSequence <= prior.Event.ReadSequence {
			return ErrStale
		}
		s.observations[e.key()] = observationProjection{id, e}
	}
	return nil
}

func loosens(before, after []DeclaredEdge, scope Scope) bool {
	for _, old := range before {
		ref, _ := ParseReference(old.Reference, scope)
		found := false
		for _, edge := range after {
			current, _ := ParseReference(edge.Reference, scope)
			if current.ChangeID != ref.ChangeID {
				continue
			}
			found = true
			if ref.Revision > 0 && (ref.Revision != current.Revision || old.PinnedHead != edge.PinnedHead) {
				return true
			}
		}
		if !found {
			return true
		}
	}
	return false
}
func (s *State) declare(id uint64, e DependenciesDeclared) error {
	old, exists := s.declarations[e.Alias.key()]
	if e.Prior != s.Prior(e.Alias) || exists && e.Poll <= old.Event.Poll {
		return ErrStale
	}
	scope := s.initialization.scope()
	for _, edge := range e.Edges {
		if _, err := ParseReference(edge.Reference, scope); err != nil {
			return err
		}
	}
	// The same durable change can never migrate queues or aliases. Retargeting
	// supplies a NEW change ID but MUST compare against this alias's prior basis.
	for _, d := range s.declarations {
		if d.Event.ChangeID == e.ChangeID && (d.Event.Alias != e.Alias || d.Event.Queue != e.Queue) {
			return ErrConflict
		}
	}
	for _, binding := range e.Bindings {
		for _, prior := range old.Event.Bindings {
			if binding.Input == prior.Input && binding.Reference != prior.Reference {
				return ErrStale
			}
		}
	}
	for _, edge := range e.Edges {
		for _, prior := range old.Event.Edges {
			if edge.Reference == prior.Reference && (edge.Queue != prior.Queue || edge.Baseline != prior.Baseline || edge.PinnedHead != prior.PinnedHead) {
				return ErrStale
			}
		}
	}
	changed := !exists || e.Digest != old.Event.Digest
	if exists && !changed && e.ChangeID == old.Event.ChangeID && e.Revision == old.Event.Revision && !e.Redeclaration && sameAccountability(e.Accountability, old.Event.Accountability) && slices.Equal(e.Bindings, old.Event.Bindings) {
		return ErrStale
	}
	if e.Redeclaration && (e.Attribution.Unknown || len(e.Attribution.Editors) == 0 || !slices.Contains(e.Attribution.Editors, e.ReadGrant.SubjectID)) {
		return ErrHeld
	}
	d := cloneDeclaration(old)
	expected := s.declarationHold(e)
	if e.Hold.Held != expected.Held || e.Hold.Unknown != expected.Unknown || !slices.Equal(e.Hold.Editors, expected.Editors) {
		return ErrHeld
	}
	d.Held = expected.Held
	d.Unknown = expected.Unknown
	d.Editors = slices.Clone(expected.Editors)
	if d.Ack != nil && d.Ack.Kind == "dependency_declaration_self_acknowledged" && !s.policy(e).Event.selfAckEnabled() {
		d.Ack.Revoked = true
	}
	d.EventID = id
	d.Event = e
	d = cloneDeclaration(d)
	if d.Editors == nil {
		d.Editors = []string{}
	}
	s.declarations[e.Alias.key()] = d
	for k, o := range s.observations {
		if o.Event.Alias == e.Alias {
			delete(s.observations, k)
		}
	}
	return nil
}
func exclusions(d declarationProjection) []string {
	return unionIDs(d.Editors, []string{d.Event.Accountability.Author, d.Event.Accountability.Submitter}, d.Event.Accountability.Humans)
}
func (s *State) acknowledge(id uint64, e Acknowledged, self bool, overridden []string) error {
	d, ok := s.declarations[e.Alias.key()]
	if !ok || !d.Held || e.Declaration != s.Prior(e.Alias) {
		return ErrStale
	}
	if d.Unknown || !slices.Equal(exclusions(d), e.Exclusions) {
		return ErrHeld
	}
	p := s.policy(d.Event)
	if e.PolicyEventID != p.EventID || e.GoverningCommit != p.Event.Commit || e.PolicyDigest != p.Event.Digest {
		return ErrStale
	}
	excluded := slices.Contains(e.Exclusions, e.Writer.SubjectID)
	if self && (!excluded || !p.Event.selfAckEnabled()) || !self && excluded {
		return ErrHeld
	}
	d = cloneDeclaration(d)
	d.Held = false
	kind := "dependency_declaration_acknowledged"
	if self {
		kind = "dependency_declaration_self_acknowledged"
	}
	if overridden == nil {
		overridden = []string{}
	}
	d.Ack = &ackProjection{id, kind, e, clonePolicy(p.Event), slices.Clone(overridden), false}
	d.Ack.Event.Exclusions = slices.Clone(e.Exclusions)
	s.declarations[e.Alias.key()] = d
	return nil
}

func sameAccountability(a, b Accountability) bool {
	return a.Author == b.Author && a.Submitter == b.Submitter && slices.Equal(a.Humans, b.Humans)
}

func (s State) declarationHold(e DependenciesDeclared) DeclarationHold {
	old, exists := s.declarations[e.Alias.key()]
	h := DeclarationHold{old.Held, old.Unknown, slices.Clone(old.Editors)}
	if h.Editors == nil {
		h.Editors = []string{}
	}
	if loosens(old.Event.Edges, e.Edges, s.initialization.scope()) {
		if !h.Held {
			h.Editors = []string{}
			h.Unknown = false
		}
		h.Held = true
	}
	if h.Held && (!exists || e.Digest != old.Event.Digest || e.Redeclaration) {
		h.Editors = unionIDs(h.Editors, e.Attribution.Editors)
		h.Unknown = e.Attribution.Unknown || len(e.Attribution.Editors) == 0
	}
	if old.Ack != nil && old.Ack.Kind == "dependency_declaration_self_acknowledged" && !s.policy(e).Event.selfAckEnabled() {
		h.Held = true
	}
	return h
}
