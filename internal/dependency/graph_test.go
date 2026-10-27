package dependency

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func graphFixture(count int) Graph {
	g := Graph{Registry: Registry{Scope: testScope(), Organization: "acme"}, Nodes: map[string]Node{}}
	for i := 0; i < count; i++ {
		g.Registry.Repositories = append(g.Registry.Repositories, fmt.Sprintf("https://github.com/acme/r%d", i))
	}
	for i := 0; i < count; i++ {
		ref := testRef(i, 0)
		g.Nodes[ref.ChangeID] = Node{Reference: ref, Queue: QueueKey{g.Registry.Repositories[i], "main", ""}, Policy: &Policy{Mode: "ordered", AllowFrom: slices.Clone(g.Registry.Repositories), AllowTo: slices.Clone(g.Registry.Repositories)}, Strict: true, Complete: true}
	}
	return g
}

func connect(g *Graph, from int, to ...int) {
	n := g.Nodes[testRef(from, 0).ChangeID]
	for _, id := range to {
		n.Edges = append(n.Edges, testRef(id, 0))
	}
	g.Nodes[n.Reference.ChangeID] = n
}
func hasCode(p Projection, code string) bool {
	for _, d := range p.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}
func evaluate(g Graph) Projection { return g.Evaluate(testRef(0, 0).ChangeID) }

func TestBilateralGrants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Graph)
		allow  bool
	}{
		{"bilateral", func(*Graph) {}, true},
		{"consumer missing", func(g *Graph) { g.Nodes[testRef(0, 0).ChangeID].Policy.AllowFrom = nil }, false},
		{"source missing", func(g *Graph) { g.Nodes[testRef(1, 0).ChangeID].Policy.AllowTo = nil }, false},
		{"no transitive grant", func(g *Graph) {
			g.Nodes[testRef(0, 0).ChangeID].Policy.AllowFrom = []string{"https://github.com/acme/r2"}
		}, false},
		{"disabled", func(g *Graph) {
			n := g.Nodes[testRef(0, 0).ChangeID]
			n.Policy = nil
			g.Nodes[n.Reference.ChangeID] = n
		}, false},
		{"consumer not strict", func(g *Graph) {
			n := g.Nodes[testRef(0, 0).ChangeID]
			n.Strict = false
			g.Nodes[n.Reference.ChangeID] = n
		}, false},
		{"source disabled", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Policy = nil
			g.Nodes[n.Reference.ChangeID] = n
		}, false},
		{"post merge", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Strict = false
			g.Nodes[n.Reference.ChangeID] = n
		}, false},
		{"unserved", func(g *Graph) { g.Registry.Repositories = g.Registry.Repositories[:1] }, false},
		{"other organization", func(g *Graph) { g.Registry.Organization = "other" }, false},
		{"same repository", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Queue.Repository = g.Registry.Repositories[0]
			g.Nodes[n.Reference.ChangeID] = n
		}, false},
		{"unserved grant", func(g *Graph) {
			g.Nodes[testRef(0, 0).ChangeID].Policy.AllowFrom = append(g.Nodes[testRef(0, 0).ChangeID].Policy.AllowFrom, "https://github.com/acme/external")
		}, false},
		{"unsupported mode", func(g *Graph) { g.Nodes[testRef(1, 0).ChangeID].Policy.Mode = "co-tested" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graphFixture(3)
			connect(&g, 0, 1)
			tc.change(&g)
			consumer, source := g.Nodes[testRef(0, 0).ChangeID], g.Nodes[testRef(1, 0).ChangeID]
			if err := BilateralGrant(g.Registry, consumer.Queue, consumer.Policy, consumer.Strict, source.Queue, source.Policy, source.Strict); (err == nil) != tc.allow {
				t.Fatalf("BilateralGrant: %v; allow %v", err, tc.allow)
			}
			p := evaluate(g)
			if p.Valid() != tc.allow {
				t.Fatalf("%+v", p)
			}
		})
	}
	// Grants on the first edge cannot authorize the next edge.
	g := graphFixture(3)
	connect(&g, 0, 1)
	connect(&g, 1, 2)
	g.Nodes[testRef(2, 0).ChangeID].Policy.AllowTo = nil
	if p := evaluate(g); p.Valid() {
		t.Fatal("transitive grant inferred")
	}
}

func TestCycleDiagnosticsDerivedAndDeterministic(t *testing.T) {
	g := graphFixture(5)
	connect(&g, 0, 2, 1)
	connect(&g, 1, 2)
	connect(&g, 2, 0)
	before := slices.Clone(g.Nodes[testRef(0, 0).ChangeID].Edges)
	p := evaluate(g)
	if !hasCode(p, "dependency-cycle") {
		t.Fatalf("%+v", p)
	}
	want := []string{testRef(0, 0).ChangeID, testRef(1, 0).ChangeID, testRef(2, 0).ChangeID, testRef(0, 0).ChangeID}
	if !reflect.DeepEqual(p.Diagnostics[0].Path, want) {
		t.Fatalf("path: %#v", p.Diagnostics)
	}
	if !reflect.DeepEqual(before, g.Nodes[testRef(0, 0).ChangeID].Edges) {
		t.Fatal("mutated declaration")
	}
	n := g.Nodes[testRef(0, 0).ChangeID]
	slices.Reverse(n.Edges)
	g.Nodes[n.Reference.ChangeID] = n
	if got := evaluate(g); !reflect.DeepEqual(got, p) {
		t.Fatalf("order changed projection: %+v vs %+v", got, p)
	}
	// Unrelated components are not blocked by a different root's cycle.
	if p := g.Evaluate(testRef(4, 0).ChangeID); !p.Valid() {
		t.Fatal(p)
	}
	self := graphFixture(1)
	connect(&self, 0, 0)
	if p := evaluate(self); len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != "dependency-cycle" || !reflect.DeepEqual(p.Diagnostics[0].Path, []string{testRef(0, 0).ChangeID, testRef(0, 0).ChangeID}) {
		t.Fatalf("self edge must diagnose cycle: %+v", p)
	}
}

func TestIncompleteAndInvalidGraphsBlock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Graph)
	}{
		{"missing", func(g *Graph) { delete(g.Nodes, testRef(1, 0).ChangeID) }},
		{"incomplete", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Complete = false
			g.Nodes[n.Reference.ChangeID] = n
		}},
		{"foreign", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Reference.Scope.Installation = "other"
			g.Nodes[n.Reference.ChangeID] = n
		}},
		{"pinned node identity", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Reference.Revision = 7
			g.Nodes[n.Reference.ChangeID] = n
		}},
		{"wrong id", func(g *Graph) {
			n := g.Nodes[testRef(1, 0).ChangeID]
			n.Reference.ChangeID = testRef(2, 0).ChangeID
			g.Nodes[testRef(1, 0).ChangeID] = n
		}},
		{"conflict", func(g *Graph) {
			n := g.Nodes[testRef(0, 0).ChangeID]
			n.Edges = append(n.Edges, testRef(1, 7))
			g.Nodes[n.Reference.ChangeID] = n
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graphFixture(2)
			connect(&g, 0, 1)
			tc.change(&g)
			if p := evaluate(g); p.Valid() {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}

func TestMatchingLandingIsTerminalButRetainsConsent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pin     uint64
		landing Landing
		revoke  bool
		valid   bool
	}{
		{"revisionless", 0, Landing{7, true, true}, false, true},
		{"matching pin", 7, Landing{7, true, true}, false, true},
		{"different pin", 8, Landing{7, true, true}, false, false},
		{"not on base", 0, Landing{7, true, false}, false, false},
		{"unconfirmed", 0, Landing{7, false, true}, false, false},
		{"no revision", 0, Landing{0, true, true}, false, false},
		{"revoked", 0, Landing{7, true, true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graphFixture(2)
			n := g.Nodes[testRef(0, 0).ChangeID]
			n.Edges = []Reference{testRef(1, tc.pin)}
			g.Nodes[n.Reference.ChangeID] = n
			n = g.Nodes[testRef(1, 0).ChangeID]
			n.Complete = false
			n.Edges = []Reference{testRef(0, 0)}
			n.Landing = tc.landing
			if tc.revoke {
				n.Policy.AllowTo = nil
			}
			g.Nodes[n.Reference.ChangeID] = n
			if p := evaluate(g); p.Valid() != tc.valid {
				t.Fatalf("%+v", p)
			}
		})
	}
}

func TestDirectEdgeLimitAfterDedupe(t *testing.T) {
	var refs []Reference
	for i := 1; i <= MaxDirectEdges; i++ {
		refs = append(refs, testRef(i, 0), testRef(i, 0))
	}
	if got, err := Normalize(refs, testScope()); err != nil || len(got) != MaxDirectEdges {
		t.Fatalf("%d %v", len(got), err)
	}
	refs = append(refs, testRef(33, 0))
	if _, err := Normalize(refs, testScope()); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	g := graphFixture(34)
	for i := 1; i <= 33; i++ {
		connect(&g, 0, i)
	}
	if p := evaluate(g); p.Valid() {
		t.Fatal("direct overflow accepted")
	}
}

func TestGraphNodeLimit(t *testing.T) {
	for _, count := range []int{256, 257} {
		g := graphFixture(count)
		for i := 1; i <= 8; i++ {
			connect(&g, 0, i)
		}
		for i := 9; i < count; i++ {
			connect(&g, 1+(i-9)/32, i)
		}
		p := evaluate(g)
		if (count == 256) != p.Valid() || p.Nodes != 256 {
			t.Fatalf("count %d: %+v", count, p)
		}
		if count == 257 && !hasCode(p, "dependency-limit-nodes") {
			t.Fatal(p)
		}
	}
}

func TestGraphEdgeLimit(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		g := graphFixture(65)
		for i := 1; i <= 32; i++ {
			connect(&g, 0, i)
			for j := 33; j < 48; j++ {
				connect(&g, i, j)
			}
		}
		if overflow {
			connect(&g, 1, 48)
		}
		p := evaluate(g)
		if p.Valid() == overflow || p.Edges != 512 {
			t.Fatalf("overflow %v: %+v", overflow, p)
		}
		if overflow && !hasCode(p, "dependency-limit-edges") {
			t.Fatal(p)
		}
	}
}

func TestGraphDepthLimitAndSharedLongPath(t *testing.T) {
	for _, count := range []int{17, 18} {
		g := graphFixture(count)
		for i := 0; i < count-1; i++ {
			connect(&g, i, i+1)
		}
		p := evaluate(g)
		if p.Valid() != (count == 17) {
			t.Fatalf("count %d: %+v", count, p)
		}
	}
	g := graphFixture(20)
	connect(&g, 0, 1, 2)
	connect(&g, 1, 18)
	connect(&g, 18, 19)
	for i := 2; i < 18; i++ {
		connect(&g, i, i+1)
	}
	if p := evaluate(g); !hasCode(p, "dependency-limit-depth") {
		t.Fatalf("shared deep path hidden: %+v", p)
	}
}

// Sixteen width-four layers, with the root also reaching its three peers in
// layer zero and one shortcut to layer two: 64 reachable nodes, 244 edges.
// Sorted DFS visits the longest paths first; equal-depth paths must not expand
// their common suffix again. Fail at expansion 65 so a broken memoization guard
// is caught immediately rather than traversing exponentially many paths.
func ladderFixture() Graph {
	const width, layers = 4, 16
	g := graphFixture(width * layers)
	connect(&g, 0, 1, 2, 3, 8)
	for layer := 0; layer < layers-1; layer++ {
		for from := layer * width; from < (layer+1)*width; from++ {
			for to := (layer + 1) * width; to < (layer+2)*width; to++ {
				connect(&g, from, to)
			}
		}
	}
	return g
}

func TestGraphLadderExpansionBound(t *testing.T) {
	g := ladderFixture()
	expansions := 0
	p := g.evaluate(testRef(0, 0).ChangeID, func(string) {
		expansions++
		if expansions > 64 {
			t.Fatal("equal/shorter path expanded a shared suffix again")
		}
	})
	if !p.Valid() || p.Nodes != 64 || p.Edges != 244 || p.Depth != 16 || expansions != 64 {
		t.Fatalf("expansions %d; projection %+v", expansions, p)
	}
}

func BenchmarkEvaluateLadder1024Served(b *testing.B) {
	g := ladderFixture()
	for i := len(g.Registry.Repositories); i < 1024; i++ {
		g.Registry.Repositories = append(g.Registry.Repositories, fmt.Sprintf("https://github.com/acme/r%d", i))
	}
	for _, n := range g.Nodes {
		n.Policy.AllowFrom = slices.Clone(g.Registry.Repositories)
		n.Policy.AllowTo = slices.Clone(g.Registry.Repositories)
	}
	b.ReportAllocs()
	for b.Loop() {
		if p := evaluate(g); !p.Valid() {
			b.Fatal(p)
		}
	}
}
