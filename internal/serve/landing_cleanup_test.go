package serve

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type lostLandingResult struct{ forge.Git }

func (g lostLandingResult) Land(ctx context.Context, repo forge.RepoRef, request forge.LandRequest, opts forge.PushOptions) (string, error) {
	_, err := g.Git.Land(ctx, repo, request, opts)
	if err != nil {
		return "", err
	}
	return "", errors.New("landing result lost")
}

func TestLandingCleanupPolicySurvivesConfigurationChange(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		legacy, missingPolicy bool
		deleteAtStart         bool
		settled               bool
	}{
		{name: "legacy", legacy: true, missingPolicy: true},
		{name: "digest-aware-without-policy", missingPolicy: true},
		{name: "disabled-at-start"},
		{name: "enabled-at-start", deleteAtStart: true},
		{name: "enabled-before-cleanup-retry", deleteAtStart: true, settled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := repositoryConfig + fmt.Sprintf("delete_head_branch: %v\n", tc.deleteAtStart)
			s, w, f, _ := fixture(t, contents)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			var tested string
			if tc.missingPolicy {
				// Persist an old in-flight attempt using only the existing event types.
				must(t, s.initializeFence(t.Context(), w))
				if !tc.legacy {
					must(t, s.configuration(t.Context(), w))
				}
				base, err := f.ReadRef(t.Context(), repo, "main")
				must(t, err)
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				pins := []queue.BatchPR{{PR: 1, HeadSHA: pr.HeadSHA}}
				tested, err = f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "openmerge/queue/main/b1", BaseSHA: base, PRs: []forge.BuildPR{{Number: 1, HeadSHA: pr.HeadSHA}}, Method: forge.Merge, BatchID: "b1"}, forge.PushOptions{})
				must(t, err)
				_, err = s.store.Append(t.Context(), w.id, []queue.Event{
					queue.BaseMoved{SHA: base}, queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true},
					queue.EnqueueRequested{PR: 1, At: time.Now()}, queue.BatchStarted{BatchID: "b1", BaseSHA: base, PRs: pins, At: time.Now()},
					queue.TestRefBuilt{BatchID: "b1", TestedSHA: tested, Method: queue.Merge, At: time.Now()},
					queue.LandingStarted{BatchID: "b1", AttemptID: "old-attempt", FenceToken: state.Fence, At: time.Now().Add(-time.Hour)},
				}, "")
				must(t, err)
				_, err = f.Land(t.Context(), repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: tested, Fence: "openmerge/fence/main", ExpectedFence: state.Fence, NextFenceID: "land/old-attempt", PRs: []forge.BuildPR{{Number: 1, HeadSHA: pr.HeadSHA}}}, forge.PushOptions{})
				must(t, err)
			} else {
				f.AddComment(repo, 1, "writer", "@openmerge queue")
				must(t, s.cycle(t.Context(), w))
				state, _, err := s.store.Load(t.Context(), w.id)
				must(t, err)
				if state.Active == nil || state.Active.Phase != queue.Testing {
					t.Fatal("PR did not reach testing")
				}
				tested = state.Active.TestedSHA
				f.SetCheckRuns(repo, tested, []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Success, HeadBranch: state.Active.TestRef}})
				if tc.settled {
					f.FailNext("HeadBranchForDeletion", errors.New("cleanup temporarily unavailable"))
				} else {
					w.Git = lostLandingResult{Git: f}
				}
				time.Sleep(2 * time.Millisecond)
				if err := s.cycle(t.Context(), w); err == nil {
					t.Fatal("expected interrupted landing or cleanup")
				}
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			if !tc.settled && (state.Active == nil || state.Active.Phase != queue.Landing) || tc.settled && state.Active != nil {
				t.Fatal("test did not stop at the intended landing phase")
			}
			updated := repositoryConfig + fmt.Sprintf("delete_head_branch: %v\n", !tc.deleteAtStart)
			_, err = f.PushBase(repo, "main", "change cleanup policy", map[string]string{".openmerge.yml": updated})
			must(t, err)
			w.Git = f
			s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w = s.queues[0]
			must(t, s.configuration(t.Context(), w))
			if !tc.settled {
				// Resolve directly to avoid waiting the production one-minute delay.
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				attempt := state.Active.LandingAttempt
				x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "main", AllowNoRequiredChecks: true}
				must(t, x.Execute(t.Context(), queue.ResolveLanding{BatchID: state.Active.ID, AttemptID: attempt.ID, TestedSHA: tested, FenceToken: attempt.FenceToken}, func(event queue.Event) error {
					_, err := s.store.Append(t.Context(), w.id, []queue.Event{event}, "")
					return err
				}))
			}
			must(t, s.cycle(t.Context(), w))
			state, _, err = s.store.Load(t.Context(), w.id)
			must(t, err)
			if state.Active != nil || len(state.Entries) != 0 {
				t.Fatal("restarted landing did not settle")
			}
			_, err = f.ReadRef(t.Context(), repo, pr.HeadBranch)
			if (err != nil) != tc.deleteAtStart {
				t.Fatalf("head deletion = %v, want start policy %v (tested %s)", err, tc.deleteAtStart, tested)
			}
		})
	}
}
