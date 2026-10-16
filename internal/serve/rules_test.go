package serve

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

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
	w.rulesChecked = time.Now().Add(-s.options.RulesInterval)
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if calls != 2 || state.PauseReason(queue.Config{}) != "" || state.Active == nil {
		t.Fatalf("later passing check did not resume: calls=%d state=%+v", calls, state)
	}
}

func TestRulesReadFailurePausesAndHiddenBypassOnlyWarns(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	var logs strings.Builder
	s.options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, errors.New("HTTP 403") }
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "repository-rules" || !strings.Contains(state.PauseDetail(queue.Config{}), "HTTP 403") {
		t.Fatal("read failure did not fail closed visibly")
	}
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, []string{"bypass actors hidden"}, nil }
	w.rulesChecked = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.PauseReason(queue.Config{}) != "" || !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "bypass actors hidden") {
		t.Fatalf("hidden bypass behavior: state=%+v logs=%s", state, &logs)
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
	w.rulesChecked = time.Time{}
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
