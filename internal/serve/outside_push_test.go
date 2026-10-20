package serve

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestOutsidePushObservationHistory(t *testing.T) {
	f := fake.New()
	base, _ := f.Init(repo, "main", "fence")
	head := base
	for i := range queue.OutsidePushCommitLimit + 1 {
		var err error
		head, err = f.PushBase(repo, "main", fmt.Sprintf("admin %d", i), nil)
		must(t, err)
	}
	e := outsidePush(t.Context(), f, repo, base, head)
	if !e.AncestryKnown || e.Rewrite || !e.HistoryKnown || !e.Truncated || len(e.Commits) != queue.OutsidePushCommitLimit || e.Commits[0] != head {
		t.Fatalf("fast-forward history: %+v", e)
	}
	rollback := outsidePush(t.Context(), f, repo, head, base)
	if !rollback.Rewrite || !rollback.HistoryKnown || len(rollback.Commits) != 0 {
		t.Fatalf("rewrite: %+v", rollback)
	}
	f.FailNext("CommitsBetween", errors.New("objects unavailable"))
	unknown := outsidePush(t.Context(), f, repo, base, head)
	if unknown.HistoryKnown || unknown.Truncated || len(unknown.Commits) != 0 {
		t.Fatalf("invented history: %+v", unknown)
	}
	f.FailNext("IsAncestor", errors.New("ancestry unavailable"))
	unknown = outsidePush(t.Context(), f, repo, base, head)
	if unknown.AncestryKnown || unknown.Rewrite {
		t.Fatal("unknown ancestry presented as known")
	}
	// An adapter without the optional reader is explicitly unknown too.
	unknown = outsidePush(t.Context(), nil, repo, base, head)
	if unknown.HistoryKnown {
		t.Fatal("unsupported history claimed known")
	}
}

func TestOutsidePushReconciliationAdminAndForcePush(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	original, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if len(original.RecentOutsidePushes()) != 0 {
		t.Fatal("initial observation invented a push")
	}
	tip, err := f.PushBase(repo, "main", "admin push", nil)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	must(t, f.SetRef(repo, "main", original.BaseSHA))
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := state.RecentOutsidePushes()
	if len(pushes) != 2 || pushes[0].PreviousSHA != original.BaseSHA || pushes[0].NewSHA != tip || pushes[0].Rewrite || !pushes[1].Rewrite || pushes[1].NewSHA != original.BaseSHA {
		t.Fatalf("outside pushes: %+v", pushes)
	}
	if !reflect.DeepEqual(pushes[0].Commits, []string{tip}) || !pushes[1].HistoryKnown || len(pushes[1].Commits) != 0 {
		t.Fatal("incorrect graph differences")
	}
	_, before, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	_, after, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if before != after {
		t.Fatal("unchanged tip duplicated observation")
	}
}

func TestOutsidePushSettlesOutstandingLandingFirst(t *testing.T) {
	for _, tc := range []struct {
		name          string
		land, outside bool
	}{
		{name: "own landing", land: true},
		{name: "own landing then admin push", land: true, outside: true},
		{name: "admin push while landing outstanding", outside: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			runOutsidePushAudit(t, s, w)
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			b := state.Active
			if b == nil || b.Phase != queue.Testing {
				t.Fatal("not testing")
			}
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.LandingStarted{BatchID: b.ID, AttemptID: "outside-test", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)}}, "")
			must(t, err)
			expectedPrevious := state.BaseSHA
			if tc.land {
				_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: state.BaseSHA, TestedSHA: b.TestedSHA, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/outside-test", PRs: []forge.BuildPR{{Number: 1, HeadSHA: b.PRs[0].HeadSHA}}}, forge.PushOptions{})
				must(t, err)
				expectedPrevious = b.TestedSHA
			}
			if tc.outside {
				_, err = f.PushBase(repo, "main", "admin during uncertain landing", nil)
				must(t, err)
			}
			if tc.outside {
				f.FailNext("ReadFence", errors.New("temporary settlement read failure"))
				if err := s.cycle(t.Context(), w); err == nil {
					t.Fatal("failed settlement was ignored")
				}
				pending, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if len(pending.RecentOutsidePushes()) != 0 {
					t.Fatal("classified before successful settlement")
				}
			}
			must(t, s.cycle(t.Context(), w))
			runOutsidePushAudit(t, s, w)
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			pushes := state.RecentOutsidePushes()
			if !tc.outside {
				if len(pushes) != 0 {
					t.Fatalf("own landing classified outside: %+v", pushes)
				}
				return
			}
			if len(pushes) != 1 || pushes[0].PreviousSHA != expectedPrevious {
				t.Fatalf("settlement baseline lost: %+v", pushes)
			}
			records, err := s.store.EventsAfter(t.Context(), w.id, 0)
			must(t, err)
			settled := false
			for _, record := range records {
				switch record.Event.(type) {
				case queue.Landed, queue.LandingAbandoned:
					settled = true
				case queue.OutsidePushRecorded:
					if !settled {
						t.Fatal("outside push recorded before settlement")
					}
				}
			}
		})
	}
}

func TestOutsidePushUnreadableHistoryIsDurable(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	_, err := f.PushBase(repo, "main", "admin with unreadable history", nil)
	must(t, err)
	f.FailNext("CommitsBetween", errors.New("history unavailable"))
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := state.RecentOutsidePushes()
	if len(pushes) != 1 || pushes[0].HistoryKnown || len(pushes[0].Commits) != 0 || pushes[0].Truncated {
		t.Fatalf("unknown evidence not durable: %+v", pushes)
	}
}

func TestOutsidePushDivergedRewrite(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	base, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	previous, err := f.PushBase(repo, "main", "old line", nil)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	must(t, f.SetRef(repo, "main", base.BaseSHA))
	tip, err := f.PushBase(repo, "main", "divergent line", nil)
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	runOutsidePushAudit(t, s, w)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	pushes := state.RecentOutsidePushes()
	if len(pushes) != 2 {
		t.Fatalf("pushes: %+v", pushes)
	}
	e := pushes[1]
	if e.PreviousSHA != previous || e.NewSHA != tip || !e.AncestryKnown || !e.Rewrite || !e.HistoryKnown || e.Truncated || !reflect.DeepEqual(e.Commits, []string{tip}) {
		t.Fatalf("diverged rewrite evidence: %+v", e)
	}
}
