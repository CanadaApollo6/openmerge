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

type outsidePushRefHook struct {
	*fake.Forge
	afterRead func()
}

func (g *outsidePushRefHook) ReadRef(ctx context.Context, r forge.RepoRef, ref string) (string, error) {
	tip, err := g.Forge.ReadRef(ctx, r, ref)
	if ref == "main" && err == nil && g.afterRead != nil {
		hook := g.afterRead
		g.afterRead = nil
		hook()
	}
	return tip, err
}

func TestOutsidePushPeerSettlementAfterRefRead(t *testing.T) {
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: "+method+"\n")
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			before, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if before.Active == nil || before.Active.Phase != queue.Testing {
				t.Fatal("missing test batch")
			}
			peer, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			f.SetCheckRuns(repo, before.Active.TestedSHA, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success, HeadBranch: before.Active.TestRef}})
			time.Sleep(3 * time.Millisecond)
			var landed string
			w.Git = &outsidePushRefHook{Forge: f, afterRead: func() {
				must(t, peer.cycle(t.Context(), peer.queues[0]))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active != nil || state.BaseSHA == before.BaseSHA {
					t.Fatalf("peer failed to land: %+v", state.Active)
				}
				landed = state.BaseSHA
			}}
			must(t, s.cycle(t.Context(), w))
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			actual, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			if pushes := state.RecentOutsidePushes(); len(pushes) != 0 {
				t.Fatalf("no outside push occurred: initial=%s landed=%s actual=%s records=%+v", before.BaseSHA, landed, actual, pushes)
			}
		})
	}
}

func TestOutsidePushLandingThenRollback(t *testing.T) {
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: "+method+"\n")
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			b := state.Active
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "rollback", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)}}, "")
			must(t, err)
			head, landed := "", b.TestedSHA
			if method == "fast-forward" {
				head = b.PRs[len(b.PRs)-1].HeadSHA
				landed = head
			}
			_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: state.BaseSHA, TestedSHA: b.TestedSHA, FastForwardHeadSHA: head, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/rollback", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}})
			must(t, err)
			must(t, f.SetRef(repo, "main", state.BaseSHA))
			for i := range 3 {
				if i == 2 {
					s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
					must(t, err)
					w = s.queues[0]
				}
				var absent *executor.ErrLandingNotOnBase
				if err := s.cycle(t.Context(), w); !errors.As(err, &absent) {
					t.Fatalf("poll %d lost settlement safety error: %v", i, err)
				}
				after, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				pushes := after.RecentOutsidePushes()
				if len(pushes) != 1 || pushes[0].PreviousSHA != landed || pushes[0].NewSHA != state.BaseSHA || !pushes[0].AncestryKnown || !pushes[0].Rewrite || !pushes[0].HistoryKnown || len(pushes[0].Commits) != 0 || pushes[0].VerificationBeforeLanding != "none" {
					t.Fatalf("poll %d rewrite evidence: %+v", i, pushes)
				}
				if after.BaseSHA != state.BaseSHA || after.Fence != state.Fence || after.Active == nil || after.Active.Phase != queue.Landing || !after.SourceBlocked(queue.LandingNotOnBase) {
					t.Fatalf("poll %d audit altered settlement: %+v", i, after)
				}
			}
		})
	}
}

func TestOutsidePushRestartAndLandingThenOutside(t *testing.T) {
	for _, method := range []string{"merge", "fast-forward"} {
		for _, outside := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/own", true: "/outside"}[outside], func(t *testing.T) {
				s, w, f, _ := fixture(t, repositoryConfig+"    merge_method: "+method+"\n")
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), w))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				b := state.Active
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "review-restart", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)}}, "")
				must(t, err)
				head, landed := "", b.TestedSHA
				if method == "fast-forward" {
					head = b.PRs[len(b.PRs)-1].HeadSHA
					landed = head
				}
				_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: state.BaseSHA, TestedSHA: b.TestedSHA, FastForwardHeadSHA: head, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/review-restart", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}})
				must(t, err)
				if outside {
					_, err = f.PushBase(repo, "main", "outside", nil)
					must(t, err)
				}
				s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
				must(t, err)
				must(t, s.cycle(t.Context(), s.queues[0]))
				after, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				pushes := after.RecentOutsidePushes()
				if outside && (len(pushes) != 1 || pushes[0].PreviousSHA != landed || len(pushes[0].Commits) != 1) || !outside && len(pushes) != 0 {
					t.Fatalf("unexpected outside records: %+v", pushes)
				}
			})
		}
	}
}

func TestOutsidePushTwoMovesBetweenPolls(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	before, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	b, err := f.PushBase(repo, "main", "outside one", nil)
	must(t, err)
	c, err := f.PushBase(repo, "main", "outside two", nil)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	after, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := after.RecentOutsidePushes()
	if len(pushes) != 1 || pushes[0].PreviousSHA != before.BaseSHA || pushes[0].NewSHA != c || len(pushes[0].Commits) != 2 || pushes[0].Commits[0] != c || pushes[0].Commits[1] != b {
		t.Fatalf("incomplete history: %+v", pushes)
	}
}

func TestOutsidePushBaseObservationDuringLanding(t *testing.T) {
	for _, land := range []bool{false, true} {
		t.Run(map[bool]string{false: "abandoned", true: "landed"}[land], func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			b := state.Active
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "observe-in-flight", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)}}, "")
			must(t, err)
			previous := state.BaseSHA
			if land {
				_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: state.BaseSHA, TestedSHA: b.TestedSHA, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/observe-in-flight", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}})
				must(t, err)
				previous = b.TestedSHA
			}
			tip, err := f.PushBase(repo, "main", "outside while landing", nil)
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			observed, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if observed.BaseSHA != tip || observed.Active == nil || observed.Active.Phase != queue.Landing || len(observed.RecentOutsidePushes()) != 0 {
				t.Fatalf("BaseMoved was deferred or settlement bypassed: %+v", observed)
			}
			s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w = s.queues[0]
			must(t, s.cycle(t.Context(), w))
			after, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			pushes := after.RecentOutsidePushes()
			if len(pushes) != 1 || pushes[0].PreviousSHA != previous || pushes[0].NewSHA != tip {
				t.Fatalf("lost settlement baseline: %+v", pushes)
			}
		})
	}
}
