package dependency

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// ReverseIndex is a rebuildable scheduling aid. A zero index is NOT proof of an
// empty graph. Rebuild after replay/snapshot and before dependency admission.
// Callers serialize rebuilding with journal updates; indexes pin a watermark.
type ReverseIndex struct {
	scope     Scope
	watermark uint64
	consumers map[string][]string
}

// RebuildReverse derives direct wakes from retained declarations, including
// waiting/unqueued sources. Wake propagation is scheduling, never satisfaction.
func RebuildReverse(s State, expected JournalInitialized) (ReverseIndex, error) {
	if err := s.VerifyBinding(expected); err != nil {
		return ReverseIndex{}, err
	}
	index := ReverseIndex{scope: expected.scope(), watermark: s.lastID, consumers: map[string][]string{}}
	for _, d := range s.declarations {
		for _, edge := range d.Event.Edges {
			ref, err := ParseReference(edge.Reference, index.scope)
			if err != nil {
				return ReverseIndex{}, err
			}
			index.consumers[ref.ChangeID] = unionIDs(index.consumers[ref.ChangeID], []string{d.Event.ChangeID})
		}
	}
	return index, nil
}

// Wake returns sorted unique consumers only for this index's exact state basis.
// Missed/rejected wakes are repaired by PollDeclarations, never assumed absent.
func (i ReverseIndex) Wake(source Reference, watermark uint64) ([]string, error) {
	if i.consumers == nil || i.watermark != watermark {
		return nil, ErrStale
	}
	if source.Scope != i.scope {
		return nil, ErrOutOfScope
	}
	if _, err := source.Canonical(); err != nil {
		return nil, err
	}
	return slices.Clone(i.consumers[source.ChangeID]), nil
}

// PollDeclarations enumerates current dependency-bearing changes independently
// of reverse wakes. The caller still supplies writer-command admission for each
// candidate before any reads; this does not scan forge PR bodies or enqueue A.
func PollDeclarations(s State) []DependenciesDeclared {
	out := []DependenciesDeclared{}
	for _, d := range s.declarations {
		if len(d.Event.Edges) != 0 {
			out = append(out, cloneDeclaration(d).Event)
		}
	}
	slices.SortFunc(out, func(a, b DependenciesDeclared) int { return strings.Compare(a.ChangeID, b.ChangeID) })
	return out
}

// ReconcileEdges performs one bounded, poll-ordered acquisition for an enqueued
// consumer. Every declared edge must have an exact request; no permissive partial
// graph is returned. A source read failure is an unavailable per-edge observation
// and does not prevent unrelated edges/queues from being polled. Apply's CAS and
// sequence checks fence delayed reads. There is no timer, runtime loop or emission.
func ReconcileEdges(ctx context.Context, reader forge.DependencyReader, s State, requests []ObservationRequest) ([]ScopedObservation, error) {
	if err := s.validateRequestSet(requests); err != nil {
		return nil, err
	}
	for _, r := range requests {
		if _, err := r.validate(); err != nil {
			return nil, err
		}
	}
	out := make([]ScopedObservation, 0, len(requests))
	for _, r := range requests {
		o, err := ObserveEdge(ctx, reader, r)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// validateRequestSet checks completeness and one shared acquisition basis before
// per-edge validation or I/O. Neither a partial graph nor mixed polls may escape.
func (s State) validateRequestSet(requests []ObservationRequest) error {
	if len(requests) == 0 || len(requests) > MaxDirectEdges {
		return ErrLimit
	}
	first := requests[0]
	d, ok := s.Declaration(first.Consumer.Alias)
	if !ok || len(requests) != len(d.Edges) {
		return ErrStale
	}
	seen := map[string]bool{}
	for _, r := range requests {
		if !reflect.DeepEqual(r.Consumer, d) || r.Declaration != s.Prior(d.Alias) || r.Stage != first.Stage || r.Sequence != first.Sequence || seen[r.Edge.Reference] || !slices.Contains(d.Edges, r.Edge) {
			return ErrStale
		}
		seen[r.Edge.Reference] = true
		_, prior, _ := s.Observation(Observed{Alias: d.Alias, ChangeID: d.ChangeID, Revision: d.Revision, Source: r.Edge.Reference})
		if prior != r.Prior {
			return ErrStale
		}
	}
	return nil
}
