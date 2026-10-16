package executor_test

// Ports the four reviewer reproductions from
// /tmp/openmerge-review-2b40_v5i/review_test.go to durable random attempts.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type reviewBeforeLand struct {
	forge.Git
	before func(forge.LandRequest)
}

func (g *reviewBeforeLand) Land(ctx context.Context, r forge.RepoRef, a forge.LandRequest) (string, error) {
	g.before(a)
	return g.Git.Land(ctx, r, a)
}

func reviewReady(t *testing.T, h *harness, base, fence string, pr forge.BuildPR) (queue.State, queue.Land) {
	t.Helper()
	s := queue.State{BaseBranch: "main", BaseSHA: base, Fence: fence}
	s = queue.Apply(s, queue.PRObserved{PR: pr.Number, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true})
	s = queue.Apply(s, queue.EnqueueRequested{PR: pr.Number, At: epoch})
	cfg := queue.Config{MergeMethod: queue.Merge, ResolveAfter: time.Minute}
	actions := queue.Plan(cfg, s, epoch)
	must(t, h.x.Execute(context.Background(), actions[0], func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
	h.f.SetCheckRuns(repo, s.Active.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
	s = queue.Apply(s, queue.ChecksCompleted{TestedSHA: s.Active.TestedSHA, Success: true})
	actions = queue.Plan(cfg, s, epoch)
	return s, actions[0].(queue.Land)
}

func reviewStartPaused(t *testing.T, h *harness, s *queue.State, a queue.Land) queue.ResolveLanding {
	t.Helper()
	h.f.FailNext("Land", errIO)
	mustError := h.x.Execute(context.Background(), a, func(e queue.Event) error { *s = queue.Apply(*s, e); return nil })
	if !errors.Is(mustError, errIO) {
		t.Fatalf("pause before push: %v", mustError)
	}
	actions := queue.Plan(queue.Config{ResolveAfter: time.Minute}, *s, epoch.Add(2*time.Minute))
	for _, action := range actions {
		if resolve, ok := action.(queue.ResolveLanding); ok {
			return resolve
		}
	}
	t.Fatal("recorded attempt was not recovered through ResolveLanding")
	return queue.ResolveLanding{}
}

func TestReviewConcurrentLandingRejection(t *testing.T) {
	h := setup(t)
	h.x.NewID = nil
	ctx := context.Background()
	s, a := reviewReady(t, h, h.base, h.fence, h.prs[0])
	h.x.Git = &reviewBeforeLand{Git: h.f, before: func(req forge.LandRequest) {
		// Another copy of this recorded attempt wins, then loses its result event.
		_, err := h.f.Land(ctx, repo, req)
		must(t, err)
	}}
	var result queue.Event
	must(t, h.x.Execute(ctx, a, func(e queue.Event) error { result = e; s = queue.Apply(s, e); return nil }))
	if _, ok := result.(queue.Landed); !ok || len(s.Entries) != 0 || s.Active != nil {
		t.Fatalf("landed base=%s but emitted %T; queued=%d; winner result lost", h.read(t, "main"), result, len(s.Entries))
	}
}

func TestReviewRebuiltStateFalseLanded(t *testing.T) {
	h := setup(t)
	h.x.NewID = nil
	ctx := context.Background()
	oldState, old := reviewReady(t, h, h.base, h.fence, h.prs[0])
	var oldID string
	must(t, h.x.Execute(ctx, old, func(e queue.Event) error {
		if started, ok := e.(queue.LandingStarted); ok {
			oldID = started.AttemptID
		}
		oldState = queue.Apply(oldState, e)
		return nil
	}))
	base, fence := h.read(t, "main"), h.read(t, fenceRef)
	s, a := reviewReady(t, h, base, fence, h.prs[1])
	if a.Key() != old.Key() {
		t.Fatal("test did not reuse the Land action key")
	}
	var newID string
	must(t, h.x.Execute(ctx, a, func(e queue.Event) error {
		if started, ok := e.(queue.LandingStarted); ok {
			newID = started.AttemptID
		}
		s = queue.Apply(s, e)
		return nil
	}))
	if oldID == newID {
		t.Fatal("attempt ID repeated across queue generations")
	}
	if got := h.read(t, "main"); got != a.TestedSHA || len(s.Entries) != 0 {
		t.Fatalf("new PR removed without landing: remaining=%d, actual=%s, recorded=%s", len(s.Entries), got, s.BaseSHA)
	}
}

func TestReviewRebuiltStateFalseAbandoned(t *testing.T) {
	h := setup(t)
	h.x.NewID = nil
	ctx := context.Background()
	oldState, old := reviewReady(t, h, h.base, h.fence, h.prs[0])
	oldResolve := reviewStartPaused(t, h, &oldState, old)
	must(t, h.x.Execute(ctx, oldResolve, func(e queue.Event) error { oldState = queue.Apply(oldState, e); return nil }))
	base, fence := h.read(t, "main"), h.read(t, fenceRef)
	s, a := reviewReady(t, h, base, fence, h.prs[1])
	if a.Key() != old.Key() {
		t.Fatal("test did not rebuild b1")
	}
	resolution := reviewStartPaused(t, h, &s, a)
	if resolution.AttemptID == oldResolve.AttemptID {
		t.Fatal("attempt ID repeated across queue generations")
	}
	must(t, h.x.Execute(ctx, resolution, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
	_, err := h.f.Land(ctx, repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: a.ExpectedBaseSHA, TestedSHA: a.TestedSHA, Fence: fenceRef, ExpectedFence: a.FenceToken, NextFenceID: "land/" + resolution.AttemptID, PRs: []forge.BuildPR{h.prs[1]}})
	if !errors.Is(err, forge.ErrLeaseRejected) {
		t.Fatalf("acknowledged abandonment did not fence delayed landing: %v", err)
	}
}

func TestReviewRejectedAttemptCanStillLand(t *testing.T) {
	h := setup(t)
	h.x.NewID = nil
	ctx := context.Background()
	s, a := reviewReady(t, h, h.base, h.fence, h.prs[0])
	var paused forge.LandRequest
	h.x.Git = &reviewBeforeLand{Git: h.f, before: func(req forge.LandRequest) {
		// The first copy pauses; this copy retries after an external base update.
		paused = req
		must(t, h.f.SetRef(repo, "main", h.prs[1].HeadSHA))
	}}
	must(t, h.x.Execute(ctx, a, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
	s = queue.Apply(s, queue.DequeueRequested{PR: 1})
	actions := queue.Plan(queue.Config{MergeMethod: queue.Merge}, s, epoch)
	for _, act := range actions {
		if _, ok := act.(queue.Dequeue); ok {
			must(t, h.x.Execute(ctx, act, func(e queue.Event) error { s = queue.Apply(s, e); return nil }))
		}
	}
	if len(s.Entries) != 0 {
		t.Fatal("dequeue did not complete")
	}
	must(t, h.f.SetRef(repo, "main", a.ExpectedBaseSHA))
	_, err := h.f.Land(ctx, repo, paused)
	if !errors.Is(err, forge.ErrLeaseRejected) {
		t.Fatalf("paused worker landed after settled rejection and acknowledged dequeue: %v", err)
	}
}
