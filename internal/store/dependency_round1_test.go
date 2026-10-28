package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestDependencyFreshSettlementChangesComposition(t *testing.T) {
	f := newDepFixture(t)
	f.observe(t, "landed", f.landedA)
	claim := f.claimLand(t)
	now := time.Now()
	head := sha('d')
	landed := sha('8')
	appendEvents(t, f.s, f.a.id,
		queue.PRObserved{PR: 42, HeadSHA: head, Eligible: true, Trusted: true},
		queue.ChangeRevisionObserved{PR: 42, ChangeID: f.a.change, Revision: 2, Predecessor: 1, HeadSHA: head, Basis: queue.RevisionObservationBasis{BaseSHA: f.landedA, At: now}},
		queue.EnqueueRequested{PR: 42, At: now})
	appendEvents(t, f.s, f.a.id,
		queue.BatchStarted{BatchID: "b2", BaseSHA: f.landedA, PRs: []queue.BatchPR{{PR: 42, HeadSHA: head}}, At: now},
		queue.TestRefBuilt{BatchID: "b2", TestedSHA: landed, Method: queue.Merge, At: now},
		queue.ChecksCompleted{TestedSHA: landed, Success: true},
		queue.LandingStarted{BatchID: "b2", AttemptID: "a2", FenceToken: "next", At: now})
	id := appendEvents(t, f.s, f.a.id, queue.Landed{BatchID: "b2", NewBaseSHA: landed, NewFence: "next2"})
	state, _, err := f.s.Load(t.Context(), f.a.id)
	must(t, err)
	if state.BaseSHA != landed {
		t.Fatal("second landing fixture failed", state)
	}
	evidence := f.evidence()
	o := &evidence.Members[0].Observations[0]
	o.SourceRevision.Revision = 2
	o.SourceRevision.Head = head
	o.SourceBase = landed
	s := &evidence.Members[0].Sources[0]
	s.Revision = o.SourceRevision
	s.Commit = landed
	s.BaseSHA = landed
	s.SettlementEventID = uint64(id)
	s.Watermark = uint64(id)
	err = f.start(t.Context(), claim, evidence)
	if err == nil {
		state, _, loadErr := f.s.Load(f.ctx(t.Context()), f.c.id)
		must(t, loadErr)
		if state.Active == nil || state.Active.Phase != queue.Landing {
			t.Fatal("unexpected successful-start state", state.Active)
		}
		assertCompositionReplay(t, f)
		t.Fatal("stale composition entered Landing against different source revision; every-cut replay also accepts it")
	}
	if !errors.Is(err, ErrLandingDecisionStale) {
		t.Fatalf("unexpected refusal: %v", err)
	}
	assertCompositionReplay(t, f)
}
