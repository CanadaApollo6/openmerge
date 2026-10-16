package store

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLandPreflightReopenRemainsRetryable(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	f := fake.New()
	repo := forge.RepoRef{Path: "owner/repo"}
	base, fence := f.Init(repo, "main", "openmerge/fence/main")
	must(t, s.InitializeFence(t.Context(), q, fence))
	pr, err := f.OpenPR(repo, 1, base, true)
	must(t, err)
	appendEvents(t, s, q, queue.BaseMoved{SHA: base}, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch})
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", State: forge.Success}})
	})
	x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }}
	claim := func() ClaimedAction {
		t.Helper()
		_, err := s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
		must(t, err)
		if len(claims) != 1 {
			t.Fatalf("expected one action: %+v", claims)
		}
		return claims[0]
	}
	execute := func(c ClaimedAction) error {
		t.Helper()
		if Consequential(c.Action) {
			proceed, err := s.BeginConsequential(t.Context(), c, config, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("current action rejected")
			}
		}
		return x.Execute(t.Context(), c.Action, s.Emit(t.Context(), c, config))
	}
	for range 2 { // Build, then observe passing checks.
		c := claim()
		must(t, execute(c))
		must(t, s.Complete(t.Context(), c, Done))
	}
	c := claim()
	land := c.Action.(queue.Land)
	f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{ID: 2, Name: "ci", State: forge.Pending}})
	if err := execute(c); !errors.Is(err, executor.ErrChecksNotSuccessful) {
		t.Fatalf("missing retryable preflight result: %v", err)
	}
	must(t, s.Complete(t.Context(), c, Failed))
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing {
		t.Fatal("reopen started a landing")
	}
	// A worker can reclaim the declined action before the rerun passes.
	stale := claim()
	proceed, err := s.BeginConsequential(t.Context(), stale, config, epoch)
	must(t, err)
	if proceed || r2Status(t, s, q, c.Key) != "cancelled" {
		t.Fatal("pending checks authorized the old Land")
	}
	f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{ID: 2, Name: "ci", State: forge.Success}})
	appendEvents(t, s, q, queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true, At: epoch})
	retry := claim()
	if retry.Key != c.Key {
		t.Fatal("test did not reuse the stable Land key")
	}
	must(t, execute(retry))
	must(t, s.Complete(t.Context(), retry, Done))
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 0 || state.BaseSHA != land.TestedSHA {
		t.Fatalf("passing rerun did not land after durable reopen: %+v", state)
	}
}

func TestDelayedReopenPreservesPassingChecksOnReplay(t *testing.T) {
	s := newStore(t)
	q, land := readyLand(t, s)
	deadline := epoch.Add(config.CheckTimeout)
	appendEvents(t, s, q,
		queue.ChecksCompleted{TestedSHA: "tested", Success: true, At: deadline.Add(time.Second)},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: deadline.Add(time.Second)},
		queue.ChecksReopened{TestedSHA: "tested", At: epoch.Add(time.Second)},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: epoch.Add(time.Second)})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	for _, action := range queue.Plan(config, state, deadline.Add(2*time.Second)) {
		if action.Key() == land.Key {
			return
		}
	}
	t.Fatal("durable replay replaced a newer pass with a stale reopen")
}
