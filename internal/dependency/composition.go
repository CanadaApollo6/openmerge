package dependency

import (
	"encoding/json"
	"reflect"
	"slices"
)

// CompositionMember freezes the dependency inputs consumed before build I/O.
// Observation IDs retain the original audit evidence; unchanged refreshes do
// not invalidate a composition merely because their journal IDs differ.
type CompositionMember struct {
	Alias       SourceAlias    `json:"alias"`
	ChangeID    string         `json:"change_id"`
	Revision    uint64         `json:"revision"`
	Head        string         `json:"head"`
	Admission   AdmissionBasis `json:"admission"`
	BasisDigest string         `json:"basis_digest"`
}

// CompositionRecorded binds the exact durable admission, before its builder
// publishes a ref. Existing batch payloads remain unchanged.
type CompositionRecorded struct {
	Namespace    string              `json:"journal_namespace"`
	BatchID      string              `json:"batch_id"`
	StartEventID uint64              `json:"start_event_id"`
	BaseSHA      string              `json:"base_sha"`
	Members      []CompositionMember `json:"members"`
}

func (CompositionRecorded) dependencyEvent() {}

// Validate checks framing and requires every consumed input to precede build admission.
func (e CompositionRecorded) Validate() error {
	if !validDigestID(e.Namespace, "omj1_") || !cleanText(e.BatchID) || e.StartEventID == 0 || !objectID(e.BaseSHA) || len(e.Members) == 0 || len(e.Members) > 128 {
		return ErrMalformed
	}
	seen := map[string]bool{}
	for _, m := range e.Members {
		if !m.Alias.valid() || !validDigestID(m.ChangeID, "chg1_") || seen[m.ChangeID] || m.Revision == 0 || !objectID(m.Head) || !m.Admission.valid() || !hexDigest(m.BasisDigest) {
			return ErrMalformed
		}
		if m.Admission.Declaration.EventID >= e.StartEventID || m.Admission.PolicyEventID >= e.StartEventID || m.Admission.AcknowledgementEventID >= e.StartEventID {
			return ErrMalformed
		}
		for _, id := range m.Admission.ObservationIDs {
			if id >= e.StartEventID {
				return ErrMalformed
			}
		}
		seen[m.ChangeID] = true
	}
	return nil
}

type compositionProjection struct {
	EventID       uint64              `json:"event_id"`
	InvalidatedBy uint64              `json:"invalidated_by"`
	Event         CompositionRecorded `json:"event"`
}

func cloneComposition(e CompositionRecorded) CompositionRecorded {
	e.Members = slices.Clone(e.Members)
	for i := range e.Members {
		m := &e.Members[i]
		m.Admission.ObservationIDs = slices.Clone(m.Admission.ObservationIDs)
	}
	return e
}

// CompositionMember derives a build input exclusively from recorded facts.
func (s State) CompositionMember(change string, revision uint64, head string) (CompositionMember, error) {
	a := s.Admission(change, revision)
	if a.Code != "" {
		return CompositionMember{}, ErrNotAdmitted
	}
	m := CompositionMember{Alias: a.Alias, ChangeID: change, Revision: revision, Head: head, Admission: a.Basis}
	type source struct {
		Reference string   `json:"reference"`
		Revision  Baseline `json:"revision"`
	}
	sources := []source{}
	edges, _, _ := s.Edges(change, revision)
	for _, edge := range edges {
		o, _, _ := s.Observation(Observed{Alias: a.Alias, ChangeID: change, Revision: revision, Source: edge.Reference})
		sources = append(sources, source{o.Source, o.SourceRevision})
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		return CompositionMember{}, err
	}
	m.BasisDigest = digest("openmerge.dependency.composition-sources.v1", string(raw))
	return m, nil
}

// Composition returns a detached active companion and its immutable ID.
func (s State) Composition(batch string) (CompositionRecorded, uint64, bool) {
	c, ok := s.compositions[batch]
	return cloneComposition(c.Event), c.EventID, ok
}

// CompositionValid compares semantic dependency inputs. Even if a source later
// recovers, the first observed invalidation must retire the old composition;
// the reader latches that invalidation in the queue fold when facts change.
func (s State) CompositionValid(e CompositionRecorded) bool {
	for _, m := range e.Members {
		current, err := s.CompositionMember(m.ChangeID, m.Revision, m.Head)
		if err != nil || current.Alias != m.Alias {
			return false
		}
		a, b := current.Admission, m.Admission
		// Policy identity is semantic, while declarations/acknowledgements are acts.
		a.PolicyEventID, b.PolicyEventID = 0, 0
		a.ObservationIDs, b.ObservationIDs = nil, nil
		if !reflect.DeepEqual(a, b) || current.BasisDigest != m.BasisDigest {
			return false
		}

	}
	return true
}

// CompositionCause returns the exact side record that first retired this input.
func (s State) CompositionCause(batch string) uint64 { return s.compositions[batch].InvalidatedBy }

// LatchCompositionCause preserves the first cause through recovery and restart.
func (s State) LatchCompositionCause(batch string) State {
	c, ok := s.compositions[batch]
	if !ok || c.InvalidatedBy != 0 {
		return s
	}
	s = s.clone()
	c.InvalidatedBy = s.lastID
	s.compositions[batch] = c
	return s
}

// RetainCompositions retires derived active evidence with the queue's batches.
// The immutable companions remain in the journal; a rebuilt batch has a new ID.
func (s State) RetainCompositions(batches []string) State {
	if s.compositions == nil {
		return s
	}
	missing := false
	for id := range s.compositions {
		missing = missing || !slices.Contains(batches, id)
	}
	if !missing {
		return s
	}
	s = s.clone()
	for id := range s.compositions {
		if !slices.Contains(batches, id) {
			delete(s.compositions, id)
		}
	}
	return s
}

// Through returns the last consumed side position for cheap preparation fences.
func (s State) Through() uint64 { return s.lastID }

// HasCompositions lets legacy and terminal-only readers skip composition work.
func (s State) HasCompositions() bool { return len(s.compositions) != 0 }
