package dependency

import (
	"errors"
	"slices"
)

// Stage A walk limits count the root at depth zero and distinct normalized edges.
const (
	MaxDirectEdges = 32
	MaxNodes       = 256
	MaxEdges       = 512
	MaxDepth       = 16
)

// Landing is supplied recorded evidence, not a forge merged flag. A confirmed,
// on-base landing matching the incoming pin makes a source terminal for walking.
// The caller remains responsible for freshness and landing-start authorization.
type Landing struct {
	Revision  uint64
	Confirmed bool
	OnBase    bool
}

// Node is a complete recorded declaration and its canonical queue binding.
// Complete false means discovery is unknown, never evidence of an empty graph.
type Node struct {
	Reference Reference
	Queue     QueueKey
	Policy    *Policy
	Strict    bool
	Complete  bool
	Edges     []Reference
	Landing   Landing
}

// Graph is an immutable input snapshot. Nodes are keyed by journal-scoped change
// ID within Registry.Scope. Registry supplies scope/served repositories only;
// alias resolution is a separate operation.
type Graph struct {
	Registry Registry
	Nodes    map[string]Node
}

// Diagnostic contains authorized-only detail. Path is deterministic for fixed
// facts, regardless of map or declaration order. It is never a journal event.
type Diagnostic struct {
	Code string
	Path []string
}

// Projection describes graph validity only. No diagnostic does not authorize
// testing or landing: lifecycle, access, holds and fresh evidence are later slices.
// In particular, no partial or overflowing projection is considered complete.
type Projection struct {
	Nodes, Edges, Depth int
	Diagnostics         []Diagnostic
}

// Valid reports whether the complete bounded graph has no structural blockers.
func (p Projection) Valid() bool { return len(p.Diagnostics) == 0 }

// Evaluate derives cycle/scope/grant/limit diagnostics without admitting edits,
// mutating facts, serializing concurrent declarations or performing external I/O.
func (g Graph) Evaluate(root string) Projection {
	return g.evaluate(root, nil)
}

// onExpand is an internal observation hook for deterministic traversal-cost tests.
func (g Graph) evaluate(root string, onExpand func(string)) Projection {
	p := Projection{}
	fail := func(code string, path []string) {
		p.Diagnostics = append(p.Diagnostics, Diagnostic{code, slices.Clone(path)})
	}
	if err := g.Registry.Scope.Validate(); err != nil {
		fail("dependency-out-of-scope", []string{root})
		return p
	}
	registry := g.Registry.index()
	policyErrors := map[string]error{}
	validateEndpoint := func(id string, node Node) error {
		if err, checked := policyErrors[id]; checked {
			return err
		}
		err := registry.validateGrantEndpoint(node.Queue, node.Policy)
		policyErrors[id] = err
		return err
	}
	seenNodes := map[string]bool{}
	seenEdges := map[[2]string]bool{}
	// Revisit a shared node at greater depth so a short first path cannot hide a
	// depth overflow. There are at most MaxDepth+1 expansions per node.
	expandedDepth := map[string]int{}
	active := map[string]bool{}
	var walk func(string, []string, int, bool)
	walk = func(id string, path []string, depth int, terminal bool) {
		if depth > MaxDepth {
			fail("dependency-limit-depth", path)
			return
		}
		p.Depth = max(p.Depth, depth)
		if !seenNodes[id] {
			if p.Nodes == MaxNodes {
				fail("dependency-limit-nodes", path)
				return
			}
			seenNodes[id] = true
			p.Nodes++
		}
		node, ok := g.Nodes[id]
		if !ok {
			fail("dependency-unresolved", path)
			return
		}
		if node.Reference.ChangeID != id || node.Reference.Revision != 0 || node.Reference.Scope != g.Registry.Scope {
			fail("dependency-out-of-scope", path)
			return
		}
		if _, err := node.Reference.Canonical(); err != nil {
			fail("dependency-unresolved", path)
			return
		}
		if err := registry.validateRepo(node.Queue.Repository); err != nil || node.Queue.Base == "" {
			fail("dependency-out-of-scope", path)
			return
		}
		if terminal {
			return
		}
		if active[id] {
			fail("dependency-cycle", path)
			return
		}
		if !node.Complete {
			fail("dependency-incomplete", path)
			return
		}
		if previous, ok := expandedDepth[id]; ok && previous >= depth {
			return
		}
		expandedDepth[id] = depth
		if onExpand != nil {
			onExpand(id)
		}
		edges, err := Normalize(node.Edges, g.Registry.Scope)
		if err != nil {
			code := "dependency-unresolved"
			switch {
			case errors.Is(err, ErrLimit):
				code = "dependency-limit-direct-edges"
			case errors.Is(err, ErrConflict):
				code = "dependency-conflict"
			case errors.Is(err, ErrOutOfScope):
				code = "dependency-out-of-scope"
			}
			fail(code, path)
			return
		}
		active[id] = true
		defer delete(active, id)
		for _, ref := range edges {
			edgePath := append(slices.Clone(path), ref.ChangeID)
			edgeKey := [2]string{id, ref.ChangeID}
			if !seenEdges[edgeKey] {
				if p.Edges == MaxEdges {
					fail("dependency-limit-edges", edgePath)
					return
				}
				seenEdges[edgeKey] = true
				p.Edges++
			}
			source, ok := g.Nodes[ref.ChangeID]
			if !ok {
				fail("dependency-unresolved", edgePath)
				continue
			}
			// Self-dependencies are cycles even though same-repository grants are
			// otherwise forbidden. Report the structural cause before consent.
			if ref.ChangeID == id {
				fail("dependency-cycle", edgePath)
				continue
			}
			if validateEndpoint(id, node) != nil || validateEndpoint(ref.ChangeID, source) != nil || bilateralGrant(node.Queue, node.Policy, node.Strict, source.Queue, source.Policy, source.Strict) != nil {
				fail("dependency-out-of-scope", edgePath)
				continue
			}
			landing := source.Landing
			terminal := landing.Confirmed && landing.OnBase && landing.Revision != 0 && (ref.Revision == 0 || ref.Revision == landing.Revision)
			walk(ref.ChangeID, edgePath, depth+1, terminal)
		}
	}
	walk(root, []string{root}, 0, false)
	return p
}
