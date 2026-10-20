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

func (g checkedGit) Land(ctx context.Context, repo forge.RepoRef, request forge.LandRequest, opts forge.PushOptions) (string, error) {
	g.beforeLand(request)
	return g.Git.Land(ctx, repo, request, opts)
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
			var durableEmit func(queue.Event) error
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
				durableEmit = s.Emit(t.Context(), claim, config)
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

func TestCancelledResolveLandingReactivatesAndSettles(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	f := fake.New()
	repo := forge.RepoRef{Path: "owner/repo"}
	base, fence := f.Init(repo, "main", "openmerge/fence/main")
	must(t, s.InitializeFence(t.Context(), q, fence))
	pr, err := f.OpenPR(repo, 1, base, true)
	must(t, err)
	appendEvents(t, s, q, queue.BaseMoved{SHA: base},
		queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch})
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	now := epoch
	x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return now }}
	var durableEmit func(queue.Event) error
	lost := errors.New("lost landing result")
	// Build, read CI, and land; the forge succeeds after LandingStarted commits,
	// but the Landed result never reaches the log.
	for step := 0; step < 3; step++ {
		_, err := s.Plan(t.Context(), q, config, now)
		must(t, err)
		claims, err := s.Claim(t.Context(), 1, time.Minute, now)
		must(t, err)
		if len(claims) != 1 {
			t.Fatalf("step %d: %+v", step, claims)
		}
		claim := claims[0]
		if Consequential(claim.Action) {
			proceed, err := s.BeginConsequential(t.Context(), claim, config, now)
			must(t, err)
			if !proceed {
				t.Fatal("initial action cancelled")
			}
		}
		durableEmit = s.Emit(t.Context(), claim, config)
		err = x.Execute(t.Context(), claim.Action, func(event queue.Event) error {
			if _, ok := event.(queue.Landed); ok {
				return lost
			}
			return durableEmit(event)
		})
		completion := Done
		if step == 2 {
			if !errors.Is(err, lost) {
				t.Fatalf("landing result was not lost: %v", err)
			}
			completion = Failed
		} else {
			must(t, err)
		}
		must(t, s.Complete(t.Context(), claim, completion))
	}
	now = epoch.Add(2 * time.Minute)
	_, err = s.Plan(t.Context(), q, config, now)
	must(t, err)
	claims, err := s.Claim(t.Context(), 10, time.Minute, now)
	must(t, err)
	var resolution ClaimedAction
	for _, claim := range claims {
		switch claim.Action.(type) {
		case queue.Land:
			proceed, err := s.BeginConsequential(t.Context(), claim, config, now)
			must(t, err)
			if proceed {
				t.Fatal("repeated uncertain landing")
			}
		case queue.ResolveLanding:
			resolution = claim
		}
	}
	if resolution.Key == "" {
		t.Fatal("resolution was not planned")
	}
	laterConfig := config
	laterConfig.ResolveAfter = 5 * time.Minute
	watermark := appendEvents(t, s, q, queue.PRObserved{PR: 99, HeadSHA: "unrelated"})
	proceed, err := s.BeginConsequential(t.Context(), resolution, laterConfig, now)
	must(t, err)
	if proceed {
		t.Fatal("resolution ignored moved deadline")
	}
	var status string
	must(t, s.pool.QueryRow(t.Context(), `SELECT status FROM actions WHERE queue_id = $1 AND key = $2`, q, resolution.Key).Scan(&status))
	if status != "cancelled" {
		t.Fatalf("status = %s", status)
	}
	now = epoch.Add(6 * time.Minute)
	planned, err := s.Plan(t.Context(), q, laterConfig, now)
	must(t, err)
	if len(planned) != 1 || planned[0].Key != resolution.Key || planned[0].Watermark != watermark {
		t.Fatalf("cancelled key not reactivated at current watermark: %+v", planned)
	}
	claims, err = s.Claim(t.Context(), 1, time.Minute, now)
	must(t, err)
	if len(claims) != 1 || claims[0].Key != resolution.Key || claims[0].AttemptID == resolution.AttemptID {
		t.Fatalf("resolution not reclaimable: %+v", claims)
	}
	proceed, err = s.BeginConsequential(t.Context(), claims[0], laterConfig, now)
	must(t, err)
	if !proceed {
		t.Fatal("reactivated resolution cancelled")
	}
	must(t, x.Execute(t.Context(), claims[0].Action, s.Emit(t.Context(), claims[0], laterConfig)))
	must(t, s.Complete(t.Context(), claims[0], Done))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 0 || state.BaseSHA == base {
		t.Fatalf("landing did not settle: %+v", state)
	}
	history, err := f.History(repo, "main")
	must(t, err)
	if len(history) != 2 || state.BaseSHA != history[1].SHA {
		t.Fatalf("resolved wrong tested commit: %+v / %+v", state, history)
	}
}

var _ forge.Git = (*checkedGit)(nil)
