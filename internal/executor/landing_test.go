package executor_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLand(t *testing.T) {
	for _, tc := range []struct {
		name       string
		moveBase   bool
		moveFence  bool
		forgeError bool
		failEmitAt int
	}{
		{name: "success"},
		{name: "base lease rejected", moveBase: true},
		{name: "fence lease rejected", moveFence: true},
		{name: "infrastructure error", forgeError: true},
		{name: "in-flight emit failure", failEmitAt: 1},
		{name: "cleanup intent emit failure", failEmitAt: 2},
		{name: "landed emit failure", failEmitAt: 3},
		{name: "rejected emit failure", moveBase: true, failEmitAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			ctx := context.Background()
			tested := h.tested(t)
			if tc.moveBase {
				must(t, h.f.SetRef(repo, "main", h.prs[1].HeadSHA))
			}
			if tc.moveFence {
				_, err := h.f.AdvanceFence(ctx, repo, fenceRef, h.fence, "other-worker")
				must(t, err)
			}
			if tc.forgeError {
				h.f.FailNext("Land", errIO)
			}
			beforeBase, beforeFence := h.read(t, "main"), h.read(t, fenceRef)
			a := queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: tested, Ref: h.buildAction().Ref, FenceToken: h.fence, Method: queue.Merge}
			var events []queue.Event
			calls := 0
			err := h.x.Execute(ctx, a, func(e queue.Event) error {
				calls++
				if calls == 1 {
					want := queue.LandingStarted{BatchID: "batch", AttemptID: "attempt", FenceToken: h.fence, At: epoch}
					if e != want || h.read(t, "main") != beforeBase || h.read(t, fenceRef) != beforeFence {
						t.Fatalf("git changed before durable LandingStarted, or wrong record: %+v", e)
					}
				}
				if calls == tc.failEmitAt {
					return errLog
				}
				events = append(events, e)
				return nil
			})
			wantCalls := 3
			if tc.moveBase || tc.failEmitAt == 2 {
				wantCalls = 2
			}
			if tc.failEmitAt == 1 || tc.forgeError || tc.moveFence {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("emit calls = %d, want %d", calls, wantCalls)
			}
			switch {
			case tc.failEmitAt > 0:
				if !errors.Is(err, errLog) {
					t.Fatalf("error = %v, want emit error", err)
				}
			case tc.moveFence:
				var tampered *executor.ErrFenceTampered
				if !errors.As(err, &tampered) {
					t.Fatalf("error = %v, want fence tampering", err)
				}
			case tc.forgeError:
				if !errors.Is(err, errIO) {
					t.Fatalf("error = %v, want infrastructure error", err)
				}
			default:
				must(t, err)
			}
			if tc.failEmitAt == 1 || tc.forgeError || tc.moveFence {
				if h.read(t, "main") != beforeBase || h.read(t, fenceRef) != beforeFence {
					t.Fatal("failed or rejected landing changed refs")
				}
			} else if tc.moveBase {
				if h.read(t, "main") != beforeBase || h.read(t, fenceRef) == beforeFence {
					t.Fatal("rejection was not fenced")
				}
			} else {
				if h.read(t, "main") != tested || h.read(t, fenceRef) == beforeFence {
					t.Fatal("successful landing did not update both refs")
				}
				c, err := h.f.Commit(repo, h.read(t, fenceRef))
				must(t, err)
				if !strings.HasPrefix(c.Message, "openmerge fence land/attempt tested="+tested+" nonce=") {
					t.Fatalf("fence did not record the landing attempt: %+v", c)
				}
			}
			if err == nil {
				var result queue.Event = queue.Landed{BatchID: "batch", NewBaseSHA: tested, NewFence: h.read(t, fenceRef)}
				if tc.moveBase || tc.moveFence {
					result = queue.LandingAbandoned{BatchID: "batch", NewFence: h.read(t, fenceRef)}
				}
				want := []queue.Event{queue.LandingStarted{BatchID: "batch", AttemptID: "attempt", FenceToken: h.fence, At: epoch}, result}
				if _, landed := result.(queue.Landed); landed {
					want = []queue.Event{want[0], queue.HeadCleanupRequested{BatchID: "batch", LandedSHA: tested}, result}
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatalf("events = %#v, want %#v", events, want)
				}
			}
		})
	}
}

func TestRejectedLandReadFenceFailure(t *testing.T) {
	h := setup(t)
	a := queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: h.tested(t), FenceToken: h.fence}
	h.f.FailNext("Land", forge.ErrLeaseRejected)
	h.f.FailNext("ReadFence", errIO)
	var events []queue.Event
	err := h.x.Execute(context.Background(), a, record(&events))
	want := []queue.Event{queue.LandingStarted{BatchID: a.BatchID, AttemptID: "attempt", FenceToken: a.FenceToken, At: epoch}}
	if !errors.Is(err, errIO) || !reflect.DeepEqual(events, want) || h.read(t, "main") != h.base || h.read(t, fenceRef) != h.fence {
		t.Fatalf("failed fence read changed refs or lost the durable attempt: %v, %#v", err, events)
	}
}

func TestRecoverLostLandingResultAfterRollback(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	s, a := reviewReady(t, h, h.base, h.fence, h.prs[0])
	err := h.x.Execute(ctx, a, func(e queue.Event) error {
		if _, ok := e.(queue.Landed); ok {
			return errLog
		}
		s = queue.Apply(s, e)
		return nil
	})
	if !errors.Is(err, errLog) || h.read(t, "main") != a.TestedSHA {
		t.Fatalf("did not lose result after push: %v", err)
	}
	next := h.read(t, fenceRef)
	must(t, h.f.SetRef(repo, "main", h.base))
	h.f.FailNext("Land", errIO)
	h.f.FailNext("AdvanceFence", errIO)
	restarted := *h.x
	actions := queue.Plan(queue.Config{ResolveAfter: time.Minute}, s, epoch.Add(2*time.Minute))
	if len(actions) != 1 {
		t.Fatalf("recovery actions: %v", actions)
	}
	action, ok := actions[0].(queue.ResolveLanding)
	if !ok || action.AttemptID != s.Active.LandingAttempt.ID {
		t.Fatalf("recovery attempted a new Land: %T", actions[0])
	}
	var events []queue.Event
	for range 3 {
		err := restarted.Execute(ctx, action, record(&events))
		var absent *executor.ErrLandingNotOnBase
		if !errors.As(err, &absent) || absent.TestedSHA != a.TestedSHA || absent.BaseTip != h.base || absent.AttemptID != action.AttemptID || len(events) != 0 {
			t.Fatalf("rolled-back landing was acknowledged: %v, events=%#v", err, events)
		}
	}
	if s.Active == nil || s.Active.Phase != queue.Landing {
		t.Fatal("unverified attempt left Landing")
	}
	if h.read(t, "main") != h.base || h.read(t, fenceRef) != next {
		t.Fatal("recovery changed refs")
	}
}

func TestResolveLanding(t *testing.T) {
	for _, tc := range []struct {
		name, fenceID, failureMethod string
		baseAtTested, failEmit       bool
	}{
		{name: "unchanged fence"},
		{name: "base alone is not evidence", baseAtTested: true},
		{name: "landed fence", fenceID: "land/attempt"},
		{name: "abandoned fence", fenceID: "abandon/attempt"},
		{name: "read failure", failureMethod: "ReadFence"},
		{name: "advance failure", failureMethod: "AdvanceFence"},
		{name: "abandoned emit failure", failEmit: true},
		{name: "landed emit failure", fenceID: "land/attempt", failEmit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			ctx := context.Background()
			a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
			switch tc.fenceID {
			case "land/attempt":
				_, err := h.f.Land(ctx, repo, forge.LandRequest{
					Base: "main", ExpectedBaseSHA: h.base, TestedSHA: a.TestedSHA,
					Fence: fenceRef, ExpectedFence: h.fence, NextFenceID: tc.fenceID,
				})
				must(t, err)
			case "abandon/attempt":
				_, err := h.f.AdvanceFence(ctx, repo, fenceRef, h.fence, tc.fenceID)
				must(t, err)
			}
			if tc.baseAtTested {
				must(t, h.f.SetRef(repo, "main", a.TestedSHA))
			}
			beforeBase, beforeFence := h.read(t, "main"), h.read(t, fenceRef)
			if tc.failureMethod != "" {
				h.f.FailNext(tc.failureMethod, errIO)
			}
			var events []queue.Event
			err := h.x.Execute(ctx, a, func(e queue.Event) error {
				if tc.failEmit {
					return errLog
				}
				return record(&events)(e)
			})
			switch {
			case tc.failureMethod != "":
				if !errors.Is(err, errIO) || len(events) != 0 {
					t.Fatalf("failed operation emitted a result or lost its error: %v, %v", err, events)
				}
			case tc.failEmit:
				if !errors.Is(err, errLog) {
					t.Fatalf("error = %v, want emit error", err)
				}
			default:
				must(t, err)
			}
			next := h.read(t, fenceRef)
			if tc.fenceID != "" || tc.failureMethod != "" {
				if next != beforeFence {
					t.Fatal("recovery or failed advance changed fence")
				}
			} else {
				sha, id, _, err := h.f.ReadFence(ctx, repo, fenceRef)
				must(t, err)
				if sha == beforeFence || id != "abandon/attempt" {
					t.Fatalf("advance did not record the abandonment: SHA %s ID %q", sha, id)
				}
			}
			if h.read(t, "main") != beforeBase {
				t.Fatal("resolution changed base")
			}
			if err == nil {
				var want queue.Event = queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next}
				if tc.fenceID == "land/attempt" {
					want = queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: next}
				}
				wantEvents := []queue.Event{want}
				if _, landed := want.(queue.Landed); landed {
					wantEvents = []queue.Event{queue.HeadCleanupRequested{BatchID: a.BatchID, LandedSHA: a.TestedSHA}, want}
				}
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatalf("events = %#v, want %#v", events, want)
				}
			}
		})
	}
}

func TestResolutionAfterLostAbandonmentConverges(t *testing.T) {
	h := setup(t)
	a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
	ctx := context.Background()
	err := h.x.Execute(ctx, a, func(queue.Event) error { return errLog })
	if !errors.Is(err, errLog) {
		t.Fatalf("first resolution = %v", err)
	}
	next := h.read(t, fenceRef)
	h.f.FailNext("AdvanceFence", errIO)
	restarted := *h.x
	var events []queue.Event
	must(t, restarted.Execute(ctx, a, record(&events)))
	want := []queue.Event{queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next}}
	if !reflect.DeepEqual(events, want) || h.read(t, "main") != h.base || h.read(t, fenceRef) != next {
		t.Fatalf("unrecorded abandonment did not converge: %#v", events)
	}
}

func TestResolveFenceTampered(t *testing.T) {
	for _, id := range []string{"land/other-attempt", "abandon/other-attempt", "unrelated", ""} {
		t.Run("id="+id, func(t *testing.T) {
			h := setup(t)
			a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
			ctx := context.Background()
			var next string
			if id == "" {
				next = h.base
				must(t, h.f.SetRef(repo, fenceRef, next))
			} else {
				var err error
				next, err = h.f.AdvanceFence(ctx, repo, fenceRef, h.fence, id)
				must(t, err)
			}
			// A matching base cannot turn a foreign fence into our success.
			must(t, h.f.SetRef(repo, "main", a.TestedSHA))
			var events []queue.Event
			err := h.x.Execute(ctx, a, record(&events))
			var tampered *executor.ErrFenceTampered
			if !errors.As(err, &tampered) || len(events) != 0 {
				t.Fatalf("error = %v, events = %v, want ErrFenceTampered without an event", err, events)
			}
			wantID := id
			if id == "unrelated" || id == "land/other-attempt" {
				wantID = ""
			}
			if tampered.AttemptID != a.AttemptID || tampered.ExpectedFence != h.fence || tampered.ActualFence != next || tampered.ActualFenceID != wantID {
				t.Fatalf("tampering diagnostics lost the fence identities: %+v", tampered)
			}
			for _, detail := range []string{h.fence, next, "land/attempt", "abandon/attempt", "ID \"" + wantID + "\"", "protection"} {
				if !strings.Contains(err.Error(), detail) {
					t.Fatalf("tampering error %q omits %q", err, detail)
				}
			}
			if h.read(t, "main") != a.TestedSHA || h.read(t, fenceRef) != next {
				t.Fatal("tampering error changed refs")
			}
		})
	}
}

// raceGit runs a competing write after the fence read, before its leased advance.
type raceGit struct {
	forge.Git
	beforeAdvance   func()
	reads, advances int
}

func (g *raceGit) ReadFence(ctx context.Context, repo forge.RepoRef, ref string) (string, string, string, error) {
	g.reads++
	return g.Git.ReadFence(ctx, repo, ref)
}

func (g *raceGit) AdvanceFence(ctx context.Context, repo forge.RepoRef, ref, expected, id string) (string, error) {
	g.advances++
	if g.beforeAdvance != nil {
		g.beforeAdvance()
	}
	return g.Git.AdvanceFence(ctx, repo, ref, expected, id)
}

func TestResolveLostRaceRedecides(t *testing.T) {
	for _, winner := range []string{"land", "abandon", "other", "read failure"} {
		t.Run(winner, func(t *testing.T) {
			h := setup(t)
			ctx := context.Background()
			a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
			git := &raceGit{Git: h.f}
			h.x.Git = git
			var next string
			git.beforeAdvance = func() {
				var err error
				if winner == "land" {
					next, err = h.f.Land(ctx, repo, forge.LandRequest{
						Base: "main", ExpectedBaseSHA: h.base, TestedSHA: a.TestedSHA,
						Fence: fenceRef, ExpectedFence: h.fence, NextFenceID: "land/attempt",
					})
				} else {
					id := "abandon/attempt"
					if winner == "other" {
						id = "abandon/other-attempt"
					}
					next, err = h.f.AdvanceFence(ctx, repo, fenceRef, h.fence, id)
				}
				must(t, err)
				if winner == "read failure" {
					h.f.FailNext("ReadFence", errIO)
				}
			}
			var events []queue.Event
			err := h.x.Execute(ctx, a, record(&events))
			switch winner {
			case "other":
				var tampered *executor.ErrFenceTampered
				if !errors.As(err, &tampered) || tampered.ActualFence != next || len(events) != 0 {
					t.Fatalf("foreign race winner was acknowledged: %v, %v", err, events)
				}
			case "read failure":
				if !errors.Is(err, errIO) || len(events) != 0 {
					t.Fatalf("re-read failure was acknowledged: %v, %v", err, events)
				}
			default:
				must(t, err)
				var want queue.Event = queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next}
				if winner == "land" {
					want = queue.Landed{BatchID: a.BatchID, NewBaseSHA: a.TestedSHA, NewFence: next}
				}
				wantEvents := []queue.Event{want}
				if _, landed := want.(queue.Landed); landed {
					wantEvents = []queue.Event{queue.HeadCleanupRequested{BatchID: a.BatchID, LandedSHA: a.TestedSHA}, want}
				}
				if !reflect.DeepEqual(events, wantEvents) {
					t.Fatalf("race recovery events = %#v, want %#v", events, want)
				}
			}
			if git.reads != 2 || git.advances != 1 || h.read(t, fenceRef) != next {
				t.Fatalf("re-decide repeated writes: reads %d, advances %d", git.reads, git.advances)
			}
		})
	}
}

func TestResolveRepeatedLeaseRejectionIsBounded(t *testing.T) {
	h := setup(t)
	a := queue.ResolveLanding{BatchID: "batch", AttemptID: "attempt", TestedSHA: h.tested(t), FenceToken: h.fence}
	git := &raceGit{Git: h.f, beforeAdvance: func() {
		h.f.FailNext("AdvanceFence", forge.ErrLeaseRejected)
	}}
	h.x.Git = git
	var events []queue.Event
	err := h.x.Execute(context.Background(), a, record(&events))
	if !errors.Is(err, forge.ErrLeaseRejected) || len(events) != 0 || git.reads != 2 || git.advances != 2 {
		t.Fatalf("repeated rejection did not stop after one re-decision: %v, reads %d, advances %d", err, git.reads, git.advances)
	}
}
