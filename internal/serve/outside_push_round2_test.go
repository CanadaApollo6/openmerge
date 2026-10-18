package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func outsidePushRound2Attempt(t *testing.T, method string, depth bool) (*Server, *worker, *fake.Forge, queue.State, forge.LandRequest) {
	t.Helper()
	config := repositoryConfig + "    merge_method: " + method + "\n"
	if depth {
		config += "    speculation: {max_depth: 4}\n"
	}
	s, w, f, _ := fixture(t, config)
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	before, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	b := before.Active
	if b == nil || b.Phase != queue.Testing {
		t.Fatal("missing testing batch")
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "round2", FenceToken: before.Fence, At: time.Now().Add(-time.Hour)}}, "")
	must(t, err)
	head := ""
	if method == "fast-forward" {
		head = b.PRs[len(b.PRs)-1].HeadSHA
	}
	req := forge.LandRequest{Base: "main", ExpectedBaseSHA: before.BaseSHA, TestedSHA: b.TestedSHA, FastForwardHeadSHA: head, Fence: "openmerge/fence/main", ExpectedFence: before.Fence, NextFenceID: "land/round2", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}}
	return s, w, f, before, req
}

func TestOutsidePushRound2FurtherOutsidePushWhileLandingHeld(t *testing.T) {
	s, w, f, before, req := outsidePushRound2Attempt(t, "merge", true)
	_, err := f.Land(t.Context(), repo, req)
	must(t, err)
	must(t, f.SetRef(repo, "main", before.BaseSHA))
	var absent *executor.ErrLandingNotOnBase
	if err := s.cycle(t.Context(), w); !errors.As(err, &absent) {
		t.Fatalf("first poll: %v", err)
	}
	second, err := f.PushBase(repo, "main", "another outside push while held", nil)
	must(t, err)
	s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	w = s.queues[0]
	if err := s.cycle(t.Context(), w); !errors.As(err, &absent) {
		t.Fatalf("second poll: %v", err)
	}
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	t.Logf("first tip=%s second observed tip=%s records=%+v", before.BaseSHA, second, after.RecentOutsidePushes())
	if after.Active == nil || after.Active.Phase != queue.Landing || !after.SourceBlocked(queue.LandingNotOnBase) {
		t.Fatal("settlement safety changed")
	}
	for _, e := range after.RecentOutsidePushes() {
		if e.NewSHA == second {
			return
		}
	}
	t.Fatal("second observed outside push lost durably")
}

func TestOutsidePushRound2AwayAndBackBeforeAbandonment(t *testing.T) {
	s, w, f, before, _ := outsidePushRound2Attempt(t, "merge", true)
	middle, err := f.PushBase(repo, "main", "outside before abandonment", nil)
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	must(t, f.SetRef(repo, "main", before.BaseSHA))
	must(t, s.configuration(t.Context(), w))
	s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	w = s.queues[0]
	held, err := s.settle(t.Context(), w)
	must(t, err)
	if held {
		t.Fatal("abandonment did not settle")
	}
	// Auditing is deliberately separate from settlement.
	s.auditOutsidePushes(t.Context(), w)
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	records, err := s.store.EventsAfter(t.Context(), w.id, 0)
	must(t, err)
	moves := []string{}
	for _, record := range records {
		if e, ok := record.Event.(queue.BaseMoved); ok {
			moves = append(moves, e.SHA)
		}
	}
	t.Logf("observed intermediate=%s BaseMoved journal=%v outside=%+v", middle, moves, after.RecentOutsidePushes())
	if len(after.RecentOutsidePushes()) != 2 {
		t.Fatal("two durably observed outside moves vanished at abandonment")
	}
}

type outsidePushRound2HistoryHook struct {
	*fake.Forge
	hook func()
}

func (g *outsidePushRound2HistoryHook) CommitsBetween(ctx context.Context, r forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	commits, truncated, err := g.Forge.CommitsBetween(ctx, r, base, head, limit)
	if g.hook != nil {
		hook := g.hook
		g.hook = nil
		hook()
	}
	return commits, truncated, err
}

func TestOutsidePushRound2AbandonmentWatermarkChangesSettlement(t *testing.T) {
	s, w, f, before, req := outsidePushRound2Attempt(t, "merge", true)
	_, err := f.PushBase(repo, "main", "outside before abandonment", nil)
	must(t, err)
	must(t, s.configuration(t.Context(), w))
	w.Git = &outsidePushRound2HistoryHook{Forge: f, hook: func() {
		_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.RefreshRequested{}}, "")
		must(t, err)
	}}
	held, settleErr := s.settle(t.Context(), w)
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	_, id, _, err := f.ReadFence(t.Context(), repo, "openmerge/fence/main")
	must(t, err)
	var phase any
	if after.Active != nil {
		phase = after.Active.Phase
	}
	t.Logf("origin/main worker control: held=%t err=%v forge fence=%s state phase=%v", held, settleErr, id, phase)
	if settleErr != nil || held || id != "abandon/round2" || after.Active != nil {
		t.Fatal("unexpected atomic abandonment result")
	}
	// Settlement must not even attempt git history enrichment.
	if w.Git.(*outsidePushRound2HistoryHook).hook == nil {
		t.Fatal("settlement read audit history")
	}
	s.auditOutsidePushes(t.Context(), w)
	if w.Git.(*outsidePushRound2HistoryHook).hook != nil {
		t.Fatal("separate audit did not read history")
	}
	must(t, f.SetRef(repo, "main", before.BaseSHA))
	_, err = f.Land(t.Context(), repo, req)
	if !errors.Is(err, forge.ErrLeaseRejected) {
		t.Fatalf("delayed push was not fenced: %v", err)
	}
	held, err = s.settle(t.Context(), w)
	must(t, err)
	if held {
		t.Fatal("retry did not settle")
	}
}

type outsidePushRound2FenceHook struct {
	*fake.Forge
	hook func()
}

func (g *outsidePushRound2FenceHook) AdvanceFence(ctx context.Context, r forge.RepoRef, ref, expected, next string) (string, error) {
	if g.hook != nil {
		hook := g.hook
		g.hook = nil
		hook()
	}
	return g.Forge.AdvanceFence(ctx, r, ref, expected, next)
}
func TestOutsidePushRound2PushWinsAbandonmentThenRollback(t *testing.T) {
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			s, w, f, before, req := outsidePushRound2Attempt(t, method, true)
			w.Git = &outsidePushRound2FenceHook{Forge: f, hook: func() {
				_, err := f.Land(t.Context(), repo, req)
				must(t, err)
				must(t, f.SetRef(repo, "main", before.BaseSHA))
			}}
			var absent *executor.ErrLandingNotOnBase
			if err := s.cycle(t.Context(), w); !errors.As(err, &absent) {
				t.Fatalf("lost push winner: %v", err)
			}
			after, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if after.Active == nil || after.Active.Phase != queue.Landing || !after.SourceBlocked(queue.LandingNotOnBase) {
				t.Fatal("successful push abandoned")
			}
			records, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			for _, record := range records {
				switch record.Event.(type) {
				case queue.LandingAbandoned, queue.Landed, queue.HeadCleanupRequested:
					t.Fatalf("unsafe settlement: %#v", record.Event)
				}
			}
		})
	}
}

func TestOutsidePushRound2UnreadableLandingAncestry(t *testing.T) {
	s, w, f, before, req := outsidePushRound2Attempt(t, "merge", true)
	_, err := f.Land(t.Context(), repo, req)
	must(t, err)
	must(t, f.SetRef(repo, "main", before.BaseSHA))
	must(t, s.block(t.Context(), w, queue.LandingNotOnBase, "landing removed", before.Active.TestedSHA))
	f.FailNext("IsAncestor", errors.New("landed object was garbage collected"))
	must(t, s.auditLandingRewrite(t.Context(), w))
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := after.RecentOutsidePushes()
	if len(pushes) != 1 || pushes[0].AncestryKnown || pushes[0].Rewrite || after.Active == nil || after.Active.Phase != queue.Landing {
		t.Fatalf("unknown ancestry omitted or settlement changed: %+v", pushes)
	}
}
