package serve

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestLandingDecisionErrorsFailWorkerAction(t *testing.T) {
	t.Parallel()
	for _, failure := range []error{store.LandingDecisionRequiredError{}, store.LandingChecksRequiredError{}, executor.ErrLandingApprovals, queue.LandingChecksTooLargeError{Field: "wire event", Limit: queue.MaxLandingChecksBytes}, queue.LandingChecksInvalidError{Err: errors.New("invalid UTF-8")}} {
		t.Run(failure.Error(), func(t *testing.T) {
			testSynctest(t, func(t *testing.T) {
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
				if errors.Is(failure, queue.ErrLandingChecksTooLarge) || errors.Is(failure, queue.ErrLandingChecksInvalid) {
					reason := "landing-checks-too-large"
					if errors.Is(failure, queue.ErrLandingChecksInvalid) {
						reason = "landing-checks-invalid"
					}
					state, _, loadErr := s.store.Load(t.Context(), w.id)
					must(t, loadErr)
					if !state.SourceBlocked(queue.Operator) || !strings.Contains(state.BlockSourceDetail(queue.Operator), reason) || state.Active.Phase == queue.Landing {
						t.Fatalf("overflow did not fail closed: %+v", state)
					}
					status := f.Statuses(repo, 1)[state.Active.PRs[0].HeadSHA]
					if status.State != forge.Blocked || status.Reason != reason {
						t.Fatalf("overflow not reported: %+v", status)
					}
					records, loadErr := s.store.EventsAfter(t.Context(), w.id, 0)
					must(t, loadErr)
					for _, record := range records {
						switch record.Event.(type) {
						case queue.LandingStarted, queue.LandingChecksRecorded:
							t.Fatal("overflow started landing")
						}
					}
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
		})
	}
}
