package dependency

import (
	"errors"
	"fmt"
	"testing"
)

func TestReconcileRequestSetGuards(t *testing.T) {
	for _, kind := range []string{"complete", "partial", "duplicate", "stage", "sequence", "declaration", "edge-membership", "limit"} {
		t.Run(kind, func(t *testing.T) {
			f := journalTestFixture(t)
			f.registry.Bindings[1].Queue = f.registry.Bindings[0].Queue
			s := f.start(t)
			d := f.declaration(t, s, Input{Reference: f.ref(1, 0)}, Input{Reference: f.ref(2, 0)})
			s = applyJournal(t, s, d)
			if kind == "limit" {
				// Exercise the set boundary independently of the declaration codec's cap.
				// All requests otherwise form a complete, unique, same-basis graph.
				for n := 3; len(d.Edges) <= MaxDirectEdges; n++ {
					edge := d.Edges[0]
					ref := f.ref(1, 0)
					ref.ChangeID = fmt.Sprintf("chg1_%064x", n)
					edge.Reference = canonical(t, ref)
					d.Edges = append(d.Edges, edge)
				}
				d.Digest = DeclarationDigest(d.Edges)
				s.declarations[f.alias.key()] = declarationProjection{EventID: s.lastID, Event: d}
			}
			requests := make([]ObservationRequest, len(d.Edges))
			for i, edge := range d.Edges {
				requests[i] = ObservationRequest{Consumer: d, Edge: edge, Declaration: s.Prior(f.alias), Stage: WorkRead, Sequence: 2}
			}
			want := ErrStale
			switch kind {
			case "complete":
				want = nil
			case "partial":
				requests = requests[:1]
			case "duplicate":
				requests[1] = requests[0]
			case "stage":
				requests[1].Stage = LandingStartRead
			case "sequence":
				requests[1].Sequence++
			case "declaration":
				requests[1].Declaration.EventID++
			case "edge-membership":
				requests[1].Edge.Baseline.Head = fmt.Sprintf("%040x", 99)
			case "limit":
				want = ErrLimit
			}
			if err := s.validateRequestSet(requests); !errors.Is(err, want) {
				t.Fatalf("set validation: %v, want %v", err, want)
			}
		})
	}
}

func TestObservationConsumerScope(t *testing.T) {
	for _, kind := range []string{"valid", "namespace", "repository"} {
		t.Run(kind, func(t *testing.T) {
			f := journalTestFixture(t)
			s := f.start(t)
			r := ObservationRequest{Consumer: f.declaration(t, s, Input{Reference: f.ref(1, 0)}), Registry: f.registry}
			want := ErrOutOfScope
			switch kind {
			case "valid":
				want = nil
			case "namespace":
				r.Registry.Scope.JournalNamespace = testScope().JournalNamespace
			case "repository":
				r.Registry.Repositories = r.Registry.Repositories[:1]
			}
			if err := r.validateConsumerScope(); !errors.Is(err, want) {
				t.Fatalf("consumer scope: %v, want %v", err, want)
			}
		})
	}
}
