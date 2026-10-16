package serve

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type countingFence struct {
	forge.Git
	writes int
}

func (g *countingFence) EnsureFence(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	g.writes++
	return g.Git.EnsureFence(ctx, repo, ref)
}

func TestStartupMissingLandingGrantBlocksBeforeFenceAndRecovers(t *testing.T) {
	db := database(t)
	f := fake.New()
	f.Init(repo, "main", "openmerge/fence/main")
	g := &countingFence{Git: f}
	missing := true
	var logs strings.Builder
	target := Target{Key: store.QueueKey{Installation: "7", Repository: repo.Path, BaseBranch: "main"}, Driver: f, Git: g, BotLogin: fake.Identity,
		CheckProtection: func(context.Context, forge.MergeMethod) ([]string, []string, error) {
			return nil, nil, errors.New("classic protection HTTP 403")
		},
		CheckPermissions: func(context.Context) ([]string, []string, error) {
			if missing {
				return []string{"installed App missing permission contents: write", "installed App missing permission workflows: write"}, []string{"installed App missing permission checks: write"}, nil
			}
			return nil, nil, nil
		},
	}
	s, err := New(t.Context(), db, []Target{target}, Options{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	must(t, err)
	w := s.queues[0]
	state, _, err := db.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "repository-rules" || len(state.RulesProblems()) != 2 || g.writes != 0 || state.Fence != "" {
		t.Fatalf("missing grant attempted a write or did not block: writes=%d state=%+v", g.writes, state)
	}
	for _, diagnostic := range []string{"level=ERROR", "contents: write", "workflows: write", "level=WARN", "checks: write", "keeping previous state"} {
		if !strings.Contains(logs.String(), diagnostic) {
			t.Errorf("missing %q in logs: %s", diagnostic, &logs)
		}
	}
	missing = false
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err = db.Load(t.Context(), w.id)
	must(t, err)
	if len(state.RulesProblems()) != 0 || state.RulesSourceRevision(queue.Permissions) != 2 {
		t.Fatal("unreadable protection vetoed permission recovery")
	}
	must(t, s.initializeFence(t.Context(), w))
	state, _, err = db.Load(t.Context(), w.id)
	must(t, err)
	if len(state.RulesProblems()) != 0 || state.Fence == "" || g.writes != 1 {
		t.Fatalf("passing grants/protection did not bootstrap: writes=%d state=%+v", g.writes, state)
	}
}

func TestRulesStartupPauseSlowRecheckAndAutomaticRecovery(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	calls := 0
	bad := true
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		calls++
		if bad {
			return []string{"fence is not protected"}, nil, nil
		}
		return nil, nil, nil
	}
	// Recreate the server to prove the first check and pause happen in New.
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	s, w = restarted, restarted.queues[0]
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if calls != 1 || state.PauseReason(queue.Config{}) != "repository-rules" {
		t.Fatalf("startup calls=%d pause=%s", calls, state.PauseReason(queue.Config{}))
	}
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	f.AddComment(repo, 1, "writer", "@openmerge refresh")
	must(t, s.cycle(t.Context(), w))
	state, watermark, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if calls != 1 || state.Active != nil || state.PauseReason(queue.Config{}) != "repository-rules" || len(state.Entries) != 1 {
		t.Fatal("poll or refresh bypassed known rules failure")
	}
	must(t, s.cycle(t.Context(), w))
	_, unchanged, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if unchanged != watermark {
		t.Fatal("unchanged pause appended duplicate events")
	}
	bad = false
	w.rulesNextCheck = time.Time{}
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if calls != 2 || state.PauseReason(queue.Config{}) != "" || state.Active == nil {
		t.Fatalf("later passing check did not resume: calls=%d state=%+v", calls, state)
	}
}

func TestRulesReadFailurePreservesStateAndRetriesSooner(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	var logs strings.Builder
	s.options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	calls := 0
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		calls++
		return nil, nil, errors.New("HTTP 403")
	}
	for _, blocked := range []bool{false, true} {
		if blocked {
			_, err := s.store.Append(t.Context(), w.id, []queue.Event{queue.RulesBlocked{Problems: []string{"known problem"}}}, "")
			must(t, err)
		}
		_, watermark, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		w.rulesNextCheck, w.rulesFailures = time.Time{}, 0
		for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
			w.rulesNextCheck = time.Time{}
			before := time.Now()
			must(t, s.repositoryRules(t.Context(), w))
			if w.rulesNextCheck.Before(before.Add(delay)) || w.rulesNextCheck.After(time.Now().Add(delay)) {
				t.Fatalf("retry deadline %s did not use %s", w.rulesNextCheck, delay)
			}
			previous := calls
			must(t, s.repositoryRules(t.Context(), w))
			if calls != previous {
				t.Fatal("rules read ignored retry backoff")
			}
		}
		state, unchanged, err := s.store.Load(t.Context(), w.id)
		must(t, err)
		if unchanged != watermark || (len(state.RulesProblems()) > 0) != blocked {
			t.Fatal("failed read changed the previous rules state")
		}
	}
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, []string{"bypass actors hidden"}, nil }
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "" || w.rulesFailures != 0 || !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "keeping previous state") || !strings.Contains(logs.String(), "bypass actors hidden") {
		t.Fatalf("hidden bypass behavior: state=%+v logs=%s", state, &logs)
	}
}

func TestRulesRecoveryNeverResumesPoisonOrLandingPause(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		return []string{"fence is not protected"}, nil, nil
	}
	must(t, s.repositoryRules(t.Context(), w))
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{
		queue.ActionPoisoned{Key: "bad-action", Reason: "invalid payload"},
		queue.PauseRequested{Reason: "landing-not-on-base", Detail: "restore tested commit"},
	}, "")
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	statuses := f.Statuses(repo, 1)
	if len(statuses) == 0 {
		t.Fatal("no paused PR status was reported")
	}
	for _, status := range statuses {
		for _, reason := range []string{"repository-rules", "action-poisoned", "landing-not-on-base"} {
			if !strings.Contains(status.Reason, reason) || !strings.Contains(status.Summary, reason) {
				t.Fatalf("status hid %s: %+v", reason, status)
			}
		}
	}
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	blocks := state.Blocks(queue.Config{})
	if len(state.RulesProblems()) != 0 || len(blocks) != 2 || blocks[0].Reason != "landing-not-on-base" || blocks[1].Reason != "action-poisoned" {
		t.Fatalf("rules recovery cleared another cause: %+v", blocks)
	}
	// Replaying after restart retains both independent non-rules causes.
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	state, _, err = restarted.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.Blocks(queue.Config{})) != 2 {
		t.Fatal("restart lost unrelated pause or poison")
	}
}

func TestRulesStartupReadFailureDoesNotStopHealthyWork(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	calls := 0
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		calls++
		return nil, nil, errors.New("HTTP 503")
	}
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	w = restarted.queues[0]
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, restarted.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if calls != 1 || state.PauseReason(queue.Config{}) != "" || state.Active == nil {
		t.Fatalf("failed read stopped healthy queue or ignored backoff: calls=%d state=%+v", calls, state)
	}
}

func TestRulesDoNotClearUnrelatedPauseAndResolveLandingStillExecutes(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	must(t, s.pause(t.Context(), w, "landing-not-on-base", "manual repair needed"))
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "landing-not-on-base" {
		t.Fatal("passing rules check cleared an unrelated pause")
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ResumeRequested{}}, "")
	must(t, err)
	w.CheckRules = nil
	w.Git = missingLanding{w.Git}
	f.OnTestRef(func(sha string, _ []int) {
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success}})
	})
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	if err := s.cycle(t.Context(), w); err == nil {
		t.Fatal("expected uncertain landing")
	}
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ResumeRequested{}}, "")
	must(t, err)
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		return []string{"rules changed during landing"}, nil, nil
	}
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	// The durable start is already present. Its resolution must run even though
	// repository rules now pause all new work.
	actions, err := s.store.Plan(t.Context(), w.id, w.plannerConfig(time.Millisecond), time.Now().Add(2*time.Minute))
	must(t, err)
	for _, action := range actions {
		if _, ok := action.Action.(queue.ResolveLanding); !ok {
			continue
		}
		claims, err := s.store.ClaimQueue(t.Context(), w.id, 10, time.Minute, time.Now())
		must(t, err)
		for _, claim := range claims {
			if claim.Key == action.Key {
				must(t, s.execute(t.Context(), w, claim))
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active != nil || state.PauseReason(queue.Config{}) != "repository-rules" {
					t.Fatal("landing did not settle while paused")
				}
				return
			}
		}
	}
	t.Fatal("repository-rules pause suppressed landing resolution")
}

func TestOverlappingRulesReadsCannotClearNewerBlock(t *testing.T) {
	// An unchanged newer failure must also invalidate the older in-flight pass.
	for _, newer := range []string{"newer rules failure", "original rules failure"} {
		t.Run(newer, func(t *testing.T) {
			s, w, _, _ := fixture(t, repositoryConfig)
			w.CheckRules = func(context.Context) ([]string, []string, error) {
				return []string{"original rules failure"}, nil, nil
			}
			must(t, s.repositoryRules(t.Context(), w))
			// Independent process-local workers share the durable queue, not scheduling state.
			older := *w
			older.rulesNextCheck = time.Time{}
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			older.CheckRules = func(ctx context.Context) ([]string, []string, error) {
				close(started)
				select {
				case <-release:
					return nil, nil, nil
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
			}
			done := make(chan error, 1)
			go func() { done <- s.repositoryRules(ctx, &older) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("older read never started")
			}
			w.rulesNextCheck = time.Time{}
			w.CheckRules = func(context.Context) ([]string, []string, error) {
				return []string{newer}, nil, nil
			}
			must(t, s.repositoryRules(ctx, w))
			// Release without closing so the deferred close also handles test failures.
			select {
			case release <- struct{}{}:
			case <-ctx.Done():
				t.Fatal("older read timed out")
			}
			must(t, <-done)
			state, _, err := s.store.Load(ctx, w.id)
			must(t, err)
			if state.RulesRevision() != 2 || state.PauseDetail(queue.Config{}) != newer {
				t.Fatalf("older passing read cleared newer block: revision=%d blocks=%+v", state.RulesRevision(), state.Blocks(queue.Config{}))
			}
			w.rulesNextCheck = time.Time{}
			w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
			must(t, s.repositoryRules(ctx, w))
			state, _, err = s.store.Load(ctx, w.id)
			must(t, err)
			if state.RulesRevision() != 3 || len(state.RulesProblems()) != 0 {
				t.Fatal("fresh recovery failed after stale recovery was ignored")
			}
		})
	}
}

func TestOverlappingFailureAfterClearStillBlocks(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	w.CheckRules = func(context.Context) ([]string, []string, error) {
		return []string{"original rules failure"}, nil, nil
	}
	must(t, s.repositoryRules(t.Context(), w))
	// The slower read fails; a faster read that began at the same revision passes
	// and clears first. The failure must still block when it arrives.
	slower := *w
	slower.rulesNextCheck = time.Time{}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	slower.CheckRules = func(ctx context.Context) ([]string, []string, error) {
		close(started)
		select {
		case <-release:
			return []string{"slower rules failure"}, nil, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.repositoryRules(ctx, &slower) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("slower read never started")
	}
	w.rulesNextCheck = time.Time{}
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	must(t, s.repositoryRules(ctx, w))
	select {
	case release <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("slower read timed out")
	}
	must(t, <-done)
	state, _, err := s.store.Load(ctx, w.id)
	must(t, err)
	if state.PauseDetail(queue.Config{}) != "slower rules failure" {
		t.Fatalf("failure that overlapped a clear was discarded: revision=%d blocks=%+v", state.RulesRevision(), state.Blocks(queue.Config{}))
	}
}
