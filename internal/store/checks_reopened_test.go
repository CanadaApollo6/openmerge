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
	appendEvents(t, s, q, queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true})
	retry := claim()
	if retry.Key == c.Key || retry.Action.(queue.Land).ChecksGeneration != land.ChecksGeneration+1 {
		t.Fatal("rerun did not advance the Land generation and key")
	}
	must(t, execute(retry))
	must(t, s.Complete(t.Context(), retry, Done))
	state, _, err = s.Load(t.Context(), q)
	must(t, err)
	if state.Active != nil || len(state.Entries) != 0 || state.BaseSHA != land.TestedSHA {
		t.Fatalf("passing rerun did not land after durable reopen: %+v", state)
	}
}

func TestDelayedReopenRequiresFreshReadOnReplay(t *testing.T) {
	s := newStore(t)
	q, _ := readyLand(t, s)
	deadline := epoch.Add(config.CheckTimeout)
	appendEvents(t, s, q,
		queue.ChecksCompleted{TestedSHA: "tested", Success: true},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: deadline.Add(time.Second)},
		queue.ChecksReopened{TestedSHA: "tested", At: epoch.Add(time.Second)},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: epoch.Add(time.Second)})
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	actions := queue.Plan(config, state, deadline.Add(2*time.Second))
	if len(actions) != 1 {
		t.Fatalf("expected one refresh: %+v", actions)
	}
	if _, ok := actions[0].(queue.RefreshChecks); !ok {
		t.Fatalf("delayed reopen reused timeout evidence: %T", actions[0])
	}
}

func TestSkewedRefreshAdvancesDurableActionKey(t *testing.T) {
	s := newStore(t)
	q, land := readyLand(t, s)
	f := fake.New()
	repo := forge.RepoRef{Path: "owner/repo"}
	f.Init(repo, "main", "openmerge/fence/main")
	fast := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch.Add(5 * time.Second) }}
	proceed, err := s.BeginConsequential(t.Context(), land, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("initial Land rejected")
	}
	// The fast Land reader records a pending read with its own refresh time.
	err = fast.Execute(t.Context(), land.Action, s.Emit(t.Context(), land, config))
	if !errors.Is(err, executor.ErrChecksNotSuccessful) {
		t.Fatalf("wrong preflight result: %v", err)
	}
	must(t, s.Complete(t.Context(), land, Failed))
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch.Add(time.Second))
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("missing released Land")
	}
	proceed, err = s.BeginConsequential(t.Context(), claims[0], config, epoch.Add(time.Second))
	must(t, err)
	if proceed {
		t.Fatal("pending checks authorized stale Land")
	}
	refreshAt := epoch.Add(time.Minute + 5*time.Second)
	_, err = s.Plan(t.Context(), q, config, refreshAt)
	must(t, err)
	claims, err = s.Claim(t.Context(), 1, time.Minute, refreshAt)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("missing refresh")
	}
	first := claims[0]
	if _, ok := first.Action.(queue.RefreshChecks); !ok {
		t.Fatalf("wrong action: %T", first.Action)
	}
	slow := fast
	slow.Now = func() time.Time { return epoch.Add(time.Second) }
	// This successful pending read used to be suppressed, then marked Done;
	// the unchanged epoch made the planner reuse an unclaimable terminal key.
	must(t, slow.Execute(t.Context(), first.Action, s.Emit(t.Context(), first, config)))
	must(t, s.Complete(t.Context(), first, Done))
	now := epoch.Add(2 * time.Minute)
	work, err := s.Plan(t.Context(), q, config, now)
	must(t, err)
	if len(work) != 1 {
		t.Fatalf("new refresh not runnable: %+v", work)
	}
	claims, err = s.Claim(t.Context(), 1, time.Minute, now)
	must(t, err)
	if len(claims) != 1 {
		t.Fatal("new refresh not claimable")
	}
	next := claims[0]
	if _, ok := next.Action.(queue.RefreshChecks); !ok || next.Key == first.Key {
		t.Fatalf("completed refresh key reused: first=%s next=%+v", first.Key, next)
	}
	f.SetCheckRuns(repo, "tested", []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: land.Action.(queue.Land).Ref}})
	slow.Now = func() time.Time { return now }
	must(t, slow.Execute(t.Context(), next.Action, s.Emit(t.Context(), next, config)))
	must(t, s.Complete(t.Context(), next, Done))
	_, err = s.Plan(t.Context(), q, config, now)
	must(t, err)
	claims, err = s.Claim(t.Context(), 1, time.Minute, now)
	must(t, err)
	if len(claims) != 1 || claims[0].Key == land.Key {
		t.Fatalf("passing read failed to restore runnable Land: %+v", claims)
	}
	proceed, err = s.BeginConsequential(t.Context(), claims[0], config, now)
	must(t, err)
	if !proceed {
		t.Fatal("fresh pass did not authorize recovered Land")
	}
}

func TestLandingStartRejectsSkewedDurableReopen(t *testing.T) {
	s := newStore(t)
	q, claim := readyLand(t, s)
	land := claim.Action.(queue.Land)
	appendEvents(t, s, q,
		queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true},
		queue.ChecksRefreshed{BatchID: land.BatchID, TestedSHA: land.TestedSHA, At: epoch.Add(5 * time.Second)})
	proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("initial Land rejected")
	}
	f := fake.New()
	repo := forge.RepoRef{Path: "owner/repo"}
	f.Init(repo, "main", "openmerge/fence/main")
	f.SetCheckRuns(repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: land.Ref}})
	git := &startGit{Git: f}
	var watermark int64
	x := executor.Executor{Driver: f, Git: git, Repo: repo, Base: "main", RequiredChecks: []string{"ci"}, Now: func() time.Time { return epoch }, NewID: func() (string, error) {
		// Commit the slower reader's reopen after the passing preflight and before
		// Emit takes the queue lock to record LandingStarted.
		watermark = appendEvents(t, s, q, queue.ChecksReopened{TestedSHA: land.TestedSHA, At: epoch.Add(time.Second)})
		return "skewed-reopen", nil
	}}
	err = x.Execute(t.Context(), land, s.Emit(t.Context(), claim, config))
	assertStaleStart(t, s, q, claim, watermark, git.calls, err)
}

func TestLandingStartRejectsReopenThenDelayedPass(t *testing.T) {
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
	nextClaim := func() ClaimedAction {
		_, err := s.Plan(t.Context(), q, config, epoch)
		must(t, err)
		claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
		must(t, err)
		if len(claims) != 1 {
			t.Fatalf("expected one action, got %+v", claims)
		}
		return claims[0]
	}
	for range 2 {
		c := nextClaim()
		if Consequential(c.Action) {
			proceed, err := s.BeginConsequential(t.Context(), c, config, epoch)
			must(t, err)
			if !proceed {
				t.Fatal("setup rejected")
			}
		}
		must(t, x.Execute(t.Context(), c.Action, s.Emit(t.Context(), c, config)))
		must(t, s.Complete(t.Context(), c, Done))
	}
	claim := nextClaim()
	land := claim.Action.(queue.Land)
	proceed, err := s.BeginConsequential(t.Context(), claim, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("initial land rejected")
	}
	git := &startGit{Git: f}
	x.Git = git
	refresh := queue.RefreshChecks{BatchID: land.BatchID, TestedSHA: land.TestedSHA}
	landNow := epoch.Add(time.Second)
	x.Now = func() time.Time { return landNow }
	oldReader := x
	oldReader.Now = func() time.Time { return epoch }
	pendingReader := x
	pendingReader.Now = func() time.Time { return epoch.Add(2 * time.Second) }
	var landErr error
	must(t, oldReader.Execute(t.Context(), refresh, func(old queue.Event) error {
		if _, ok := old.(queue.ChecksCompleted); !ok {
			appendEvents(t, s, q, old)
			return nil
		}
		x.NewID = func() (string, error) {
			f.SetCheckRuns(repo, land.TestedSHA, nil)
			must(t, pendingReader.Execute(t.Context(), refresh, func(e queue.Event) error { appendEvents(t, s, q, e); return nil }))
			appendEvents(t, s, q, old)
			landNow = epoch.Add(3 * time.Second)
			return "round5-delayed-pass", nil
		}
		landErr = x.Execute(t.Context(), land, s.Emit(t.Context(), claim, config))
		return nil
	}))
	starts := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='landing_started'`, q)
	landed := count(t, s, `SELECT count(*) FROM events WHERE queue_id=$1 AND type='landed'`, q)
	if starts != 0 || git.calls != 0 || landed != 0 || r2Status(t, s, q, claim.Key) != "cancelled" {
		t.Fatalf("delayed pass revived invalidated landing: LandingStarted=%d git.Land=%d Landed=%d error=%v", starts, git.calls, landed, landErr)
	}
	if !errors.Is(landErr, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", landErr)
	}
	fresh := nextClaim()
	if fresh.Key == claim.Key || fresh.Action.(queue.Land).ChecksGeneration != land.ChecksGeneration+1 {
		t.Fatalf("new generation not planned: %+v", fresh)
	}
	proceed, err = s.BeginConsequential(t.Context(), fresh, config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("new generation rejected")
	}
	// Projected success from the delayed pass must still face a fresh CI read.
	err = x.Execute(t.Context(), fresh.Action, s.Emit(t.Context(), fresh, config))
	if !errors.Is(err, executor.ErrChecksNotSuccessful) || git.calls != 0 {
		t.Fatalf("new Land skipped its pending preflight: %v", err)
	}
}

func TestBeginConsequentialRejectsReopenThenDelayedPass(t *testing.T) {
	s := newStore(t)
	q, stale := readyLand(t, s)
	land := stale.Action.(queue.Land)
	appendEvents(t, s, q, queue.ChecksReopened{TestedSHA: land.TestedSHA, At: epoch}, queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true})
	proceed, err := s.BeginConsequential(t.Context(), stale, config, epoch)
	must(t, err)
	if proceed || r2Status(t, s, q, stale.Key) != "cancelled" {
		t.Fatal("delayed pass revived stale claim")
	}
	_, err = s.Plan(t.Context(), q, config, epoch)
	must(t, err)
	claims, err := s.Claim(t.Context(), 1, time.Minute, epoch)
	must(t, err)
	if len(claims) != 1 || claims[0].Key == stale.Key || claims[0].Action.(queue.Land).ChecksGeneration != land.ChecksGeneration+1 {
		t.Fatalf("new generation not claimable: %+v", claims)
	}
	proceed, err = s.BeginConsequential(t.Context(), claims[0], config, epoch)
	must(t, err)
	if !proceed {
		t.Fatal("new generation rejected")
	}
}
