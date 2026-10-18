package serve

import (
	"errors"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestLandingDecisionErrorsFailWorkerAction(t *testing.T) {
	for _, failure := range []error{store.LandingDecisionRequiredError{}, executor.ErrLandingApprovals} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			f.SetCheckRuns(repo, state.Active.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Success, HeadBranch: state.Active.TestRef}})
			_, err = s.store.Append(t.Context(), w.id, []queue.Event{queue.ChecksCompleted{TestedSHA: state.Active.TestedSHA, Success: true}}, "")
			must(t, err)
			now := time.Now()
			cfg := w.plannerConfig(s.options.PollInterval)
			_, err = s.store.Plan(t.Context(), w.id, cfg, now)
			must(t, err)
			claims, err := s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, now)
			must(t, err)
			var land store.ClaimedAction
			for _, claim := range claims {
				if _, ok := claim.Action.(queue.Land); ok {
					land = claim
				}
			}
			if land.Key == "" {
				t.Fatal("no land claim")
			}
			// Inject the exact gate/store error at the executor boundary; this exercises
			// the worker's real completion classification without enabling approvals.
			f.FailNext("CheckRuns", failure)
			err = s.execute(t.Context(), w, land)
			if !errors.Is(err, failure) {
				t.Fatal("failure swallowed", err)
			}
			retry, err := s.store.ClaimQueue(t.Context(), w.id, 100, time.Minute, now.Add(24*time.Hour))
			must(t, err)
			for _, claim := range retry {
				if claim.Key == land.Key {
					return
				}
			}
			t.Fatal("failed Land was completed as terminal Done")
		})
	}
}
