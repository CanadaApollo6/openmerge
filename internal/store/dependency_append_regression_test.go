package store

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Append must refuse caller-supplied companions without poisoning replay.
func TestDependencyAppendOfBadInvalidationCompanion(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	id := admitDependencyBuild(t, f, depCfg)
	ctx := f.ctx(t.Context())
	// (a) wrong-suffix BatchesInvalidated: must be refused
	_, err := f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{id}, Reason: "dependency-changed"}}, "")
	if err == nil {
		t.Fatal("accepted invalid suffix")
	}
	// (b) hand-built companion
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.DependencyCompositionsInvalidated{BatchIDs: []string{id}, CompositionIDs: []uint64{1}, CauseEventIDs: []uint64{2}, Reason: "dependency-changed"}}, "")
	if err == nil {
		t.Fatal("accepted supplied companion")
	}
	_, _, lerr := f.s.Load(ctx, f.c.id)
	t.Logf("(b) subsequent Load err=%v", lerr)
	_, _, lerr2 := f.s.Load(t.Context(), f.c.id)
	t.Logf("(b) subsequent plain Load err=%v", lerr2)
	if lerr != nil || lerr2 != nil {
		t.Fatal("POISONED: Append accepted a companion that replay rejects; queue unreadable")
	}
}

func TestDependencyInvalidationRequiresExactSuffix(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	second := addDepPR(t, f.s, f.c, 8, 'c')
	second.declare(t, f.s, f.b, dependency.Basis{}, 1, false)
	ctx := f.ctx(t.Context())
	now := time.Now()
	_, err := f.s.Append(ctx, f.c.id, []queue.Event{
		queue.ChainedBatchStarted{BatchID: "b1", BaseSHA: f.c.base, PRs: []queue.BatchPR{{PR: f.c.pr, HeadSHA: f.c.head}}, At: now, Position: 1},
		queue.ChainedTestRefBuilt{BatchID: "b1", TestedSHA: sha('5'), LandingSHA: sha('5'), Method: queue.Merge, At: now, Position: 1},
		queue.ChainedBatchStarted{BatchID: "b2", BaseSHA: sha('5'), PRs: []queue.BatchPR{{PR: second.pr, HeadSHA: second.head}}, At: now, ParentBatchID: "b1", Position: 2},
	}, "")
	must(t, err)
	f.c.applyPolicy(t, f.s, f.b, true, []string{}, []string{})
	state, _, err := f.s.Load(ctx, f.c.id)
	must(t, err)
	if got := state.DependencyInvalidationTargets(); !slices.Equal(got, []string{"b1", "b2"}) {
		t.Fatal("fixture suffix", got)
	}
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{"b2", "b1"}, Reason: "dependency-changed"}}, "")
	if !errors.Is(err, ErrStale) {
		t.Fatal("accepted reordered suffix", err)
	}
	_, err = f.s.Append(ctx, f.c.id, []queue.Event{queue.BatchesInvalidated{BatchIDs: []string{"b1", "b2"}, Reason: "dependency-changed"}}, "")
	must(t, err)
	state, _, err = f.s.Load(ctx, f.c.id)
	must(t, err)
	if len(state.Batches()) != 0 {
		t.Fatal("exact suffix did not fold", state.Batches())
	}
	assertCompositionReplay(t, f)
}
