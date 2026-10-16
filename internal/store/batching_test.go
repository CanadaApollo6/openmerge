package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestBatchStartedRoundTripAndCompositionRevalidation(t *testing.T) {
	url := databaseURL(t)
	s := openTestStore(t, url)
	q := ensureQueue(t, s, "")
	appendEvents(t, s, q, seedEvents()...)
	cfg := config
	cfg.BatchMaxSize = 8
	planned, err := s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	stale := planned[0].Action.(queue.BuildTestRef)
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	appendEvents(t, s, q, queue.PRObserved{PR: 2, HeadSHA: "head2", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: epoch})
	proceed, err := s.BeginConsequential(t.Context(), claims[0], cfg, epoch)
	must(t, err)
	if proceed {
		t.Fatal("stale composition was authorized")
	}
	planned, err = s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	build := planned[0].Action.(queue.BuildTestRef)
	if build.BatchID != stale.BatchID || build.Key() == stale.Key() || len(build.PRs) != 2 {
		t.Fatal("composition did not change key", build)
	}
	claims, err = s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	proceed, err = s.BeginConsequential(t.Context(), claims[0], cfg, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("current composition rejected")
	}
	emit := s.Emit(t.Context(), q)
	if err := emit(queue.BatchStarted{BatchID: stale.BatchID, BaseSHA: stale.BaseSHA, PRs: stale.PRs, At: epoch}); !errors.Is(err, ErrBatchStarted) {
		t.Fatalf("stale emit=%v", err)
	}
	started := queue.BatchStarted{BatchID: build.BatchID, BaseSHA: build.BaseSHA, PRs: build.PRs, At: epoch}
	must(t, emit(started))
	other := openTestStore(t, url)
	state, _, err := other.Load(t.Context(), q)
	must(t, err)
	if state.Active == nil || !reflect.DeepEqual(state.Active.PRs, build.PRs) || state.Active.BaseSHA != build.BaseSHA {
		t.Fatalf("composition lost on replay: %+v", state)
	}
	// An identical start recovers a crash in the durable-start/pre-build window.
	must(t, emit(started))
	appendEvents(t, s, q, queue.TestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested", Method: queue.Squash, At: epoch}, queue.ChecksCompleted{TestedSHA: "tested"})
	must(t, s.Complete(t.Context(), claims[0], Done))
	state, _, err = other.Load(t.Context(), q)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 2 {
		t.Fatal("failure dequeued batch members")
	}
	planned, err = s.Plan(t.Context(), q, cfg, epoch)
	must(t, err)
	// Cleanup comes first, then the first pending half.
	var next queue.BuildTestRef
	for _, a := range planned {
		if b, ok := a.Action.(queue.BuildTestRef); ok {
			next = b
		}
	}
	if len(next.PRs) != 1 || next.PRs[0].PR != 1 {
		t.Fatal("pending first half lost", planned)
	}
	appendEvents(t, s, q, queue.BatchStarted{BatchID: next.BatchID, BaseSHA: next.BaseSHA, PRs: next.PRs, At: epoch})
	state, _, err = other.Load(t.Context(), q)
	must(t, err)
	if state.Active == nil || !state.Active.FromBisection {
		t.Fatal("bisection provenance lost on replay")
	}
}
