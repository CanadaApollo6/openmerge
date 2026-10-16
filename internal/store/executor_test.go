package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type checkedGit struct {
	forge.Git
	beforeLand func(forge.LandRequest)
}

func (g checkedGit) Land(ctx context.Context, repo forge.RepoRef, request forge.LandRequest) (string, error) {
	g.beforeLand(request)
	return g.Git.Land(ctx, repo, request)
}

func TestExecutorDurablyLandsThreePRsSerially(t *testing.T) {
	for _, loseResult := range []bool{false, true} {
		name := "normal"
		if loseResult {
			name = "recover-lost-landing-result"
		}
		t.Run(name, func(t *testing.T) {
			url := databaseURL(t)
			s := openTestStore(t, url)
			q := ensureQueue(t, s, "")
			f := fake.New()
			repo := forge.RepoRef{Path: "owner/repo"}
			base, fence := f.Init(repo, "main", "openmerge/fence/main")
			must(t, s.InitializeFence(t.Context(), q, fence))
			events := []queue.Event{queue.BaseMoved{SHA: base}}
			for pr := 1; pr <= 3; pr++ {
				opened, err := f.OpenPR(repo, pr, base, true)
				must(t, err)
				events = append(events, queue.PRObserved{PR: pr, HeadSHA: opened.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: pr, At: epoch})
			}
			appendEvents(t, s, q, events...)
			memory := fold(queue.State{BaseBranch: "main", Fence: fence}, events)
			f.OnTestRef(func(sha string, _ []int) {
				f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
			})
			now := epoch
			landCalls := 0
			x := executor.Executor{
				Driver: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return now },
				Git: checkedGit{Git: f, beforeLand: func(request forge.LandRequest) {
					// This is a separate database transaction, immediately before
					// forge I/O: both fences must already be committed and visible.
					state, _, err := s.Load(t.Context(), q)
					must(t, err)
					if state.Active == nil || state.Active.Phase != queue.Landing || state.Active.LandingAttempt.ID == "" ||
						"land/"+state.Active.LandingAttempt.ID != request.NextFenceID {
						t.Fatal("forge landing preceded durable LandingStarted")
					}
					if n := count(t, s, `SELECT count(*) FROM actions WHERE queue_id = $1 AND type = 'land' AND status = 'in_flight'`, q); n != 1 {
						t.Fatal("forge landing preceded the durable in-flight record")
					}
					landCalls++
				}},
			}
			errLost := errors.New("lost landing result before durable emit")
			lost := false
			var tested, landed []string
			durableEmit := s.Emit(t.Context(), q)
			emit := func(event queue.Event) error {
				if _, ok := event.(queue.Landed); ok && loseResult && len(landed) == 1 && !lost {
					lost = true
					return errLost
				}
				if err := durableEmit(event); err != nil {
					return err
				}
				events = append(events, event)
				memory = queue.Apply(memory, event)
				state, watermark, err := s.Load(t.Context(), q)
				if err != nil {
					return err
				}
				if watermark == 0 || !reflect.DeepEqual(state, memory) {
					t.Fatal("emit returned before the result was durable or replayable")
				}
				switch e := event.(type) {
				case queue.TestRefBuilt:
					tested = append(tested, e.TestedSHA)
				case queue.Landed:
					landed = append(landed, e.NewBaseSHA)
				}
				return nil
			}
			for steps := 0; ; steps++ {
				if steps >= 100 {
					t.Fatalf("queue failed to converge: %+v", memory)
				}
				_, err := s.Plan(t.Context(), q, config, now)
				must(t, err)
				claims, err := s.Claim(t.Context(), 1, time.Minute, now)
				must(t, err)
				if len(claims) == 0 {
					break
				}
				claim := claims[0]
				if Consequential(claim.Action) {
					proceed, err := s.BeginConsequential(t.Context(), claim, config, now)
					must(t, err)
					if !proceed {
						continue
					}
				}
				err = x.Execute(t.Context(), claim.Action, emit)
				status := Done
				if err != nil {
					if !errors.Is(err, errLost) {
						t.Fatal(err)
					}
					status = Failed
					now = now.Add(2 * time.Minute)
				}
				must(t, s.Complete(t.Context(), claim, status))
			}
			if len(memory.Entries) != 0 || memory.Active != nil || len(tested) != 3 || !slices.Equal(tested, landed) || landCalls != 3 || lost != loseResult {
				t.Fatalf("incorrect serial landing: tested=%v landed=%v calls=%d lost=%v state=%+v", tested, landed, landCalls, lost, memory)
			}
			history, err := f.History(repo, "main")
			must(t, err)
			if len(history) != 4 {
				t.Fatalf("base history has %d commits, want root plus 3 tested commits", len(history))
			}
			for i, commit := range history[1:] {
				if commit.SHA != tested[i] || !slices.Equal(commit.PRs, []int{1, 2, 3}[:i+1]) {
					t.Fatalf("base commit %d did not land exactly the tested PRs: %+v", i, commit)
				}
			}
			if n := count(t, s, `SELECT count(*) FROM events WHERE queue_id = $1`, q); n != len(events) {
				t.Fatalf("durable event count = %d, emitted %d", n, len(events))
			}
			if n := count(t, s, `SELECT count(*) FROM actions WHERE status IN ('planned', 'claimed', 'in_flight')`); n != 0 {
				t.Fatalf("%d unfinished actions remain", n)
			}
			// Reopen the pool so the final projection cannot use in-memory state.
			s.Close()
			fresh := openTestStore(t, url)
			replayed, _, err := fresh.Load(t.Context(), q)
			must(t, err)
			if !reflect.DeepEqual(replayed, memory) {
				t.Fatalf("fresh Load differs: %+v / %+v", replayed, memory)
			}
			actualFence, err := f.ReadRef(t.Context(), repo, "openmerge/fence/main")
			must(t, err)
			if replayed.Fence != actualFence || replayed.BaseSHA != tested[2] {
				t.Fatal("replayed queue differs from forge base or fence")
			}
			t.Logf("Postgres: three PRs landed serially, %d events committed and replayed; lost-result recovery=%v", len(events), loseResult)
		})
	}
}
