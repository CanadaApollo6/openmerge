package serve

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCheckPolicyKeepsPendingRerunsAndAmbiguity(t *testing.T) {
	expression, err := condition.Parse("check-success = ci")
	must(t, err)
	pr := forge.PullRequest{Base: "main"}
	checks := []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Failure}, {ID: 2, Name: "ci", Source: "app:1", State: forge.Pending}}
	met, err := evaluate(expression, pr, checks)
	must(t, err)
	terminal, _, err := mergeResult(expression, pr, checks)
	must(t, err)
	if met || terminal {
		t.Fatal("stale failure overrode pending rerun")
	}
	checks[1].State = forge.Success
	met, err = evaluate(expression, pr, checks)
	must(t, err)
	if !met {
		t.Fatal("passing rerun did not supersede failure")
	}
	checks = append(checks, forge.CheckRun{ID: 3, Name: "unrequired", Source: "app:1", State: forge.Success}, forge.CheckRun{ID: 4, Name: "unrequired", Source: "app:2", State: forge.Success})
	met, err = evaluate(expression, pr, checks)
	must(t, err)
	if !met {
		t.Fatal("unrequired producer ambiguity blocked CI")
	}
	checks = append(checks, forge.CheckRun{ID: 5, Name: "ci", Source: "app:2", State: forge.Success})
	_, err = evaluate(condition.Not(expression), pr, checks)
	var ambiguity *executor.CheckSourceAmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("negation hid producer ambiguity: %v", err)
	}
}

func TestRetryTargetsUseLoadedEffectiveMergePolicy(t *testing.T) {
	epoch := time.Unix(1, 0)
	for _, accepted := range []string{"success", "skipped", "neutral"} {
		t.Run(accepted, func(t *testing.T) {
			data := []byte(`version: 1
queues:
  - name: default
    checks: {retry_failed: 1}
    merge_when:
      - check-success = ci
      - or:
          - check-success = linux
          - check-failure = linux
          - check-success = fallback
`)
			// Both ordinary success and explicitly accepted terminal states must prune
			// the optional failed commit status from the effective failures.
			if accepted == "success" {
				data = []byte(strings.Replace(string(data), "          - check-failure = linux\n", "", 1))
			}
			loaded, _, err := config.Load(data)
			must(t, err)
			expression := loaded.Queues[0].MergeWhen
			f := fake.New()
			f.Init(repo, "main", "fence")
			ref := "openmerge/queue/main/b1"
			checks := []forge.CheckRun{
				{ID: 1, Attempt: 1, Name: "ci", Source: "app:15368", State: forge.Failure, HeadBranch: ref},
				{ID: 2, Attempt: 1, Name: "linux", Source: "app:15368", State: forge.CheckState(accepted), HeadBranch: ref},
				{ID: 3, Attempt: 1, Name: "fallback", Source: "status:ci", State: forge.Failure},
			}
			f.SetCheckRuns(repo, "tested", checks)
			pr := forge.PullRequest{Base: "main"}
			x := executor.Executor{Driver: f, Repo: repo, Base: "main", ChecksRetryFailed: 1, ReferencesCheck: expression.ReferencesCheck,
				CheckPolicy: func(c []forge.CheckRun) (bool, bool, error) { return mergeResult(expression, pr, c) },
				RetryFailure: func(c []forge.CheckRun, name string) bool {
					return retryPolicyFailure(expression, []forge.PullRequest{pr}, c, name)
				},
			}
			state := queue.State{BaseBranch: "main"}
			for _, event := range []queue.Event{
				queue.BaseMoved{SHA: "base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: epoch},
				queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 1, HeadSHA: "head"}}, At: epoch},
				queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", Method: queue.Merge, At: epoch},
			} {
				state = queue.Apply(state, event)
			}
			emit := func(event queue.Event) error { state = queue.Apply(state, event); return nil }
			must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b1", TestedSHA: "tested"}, emit))
			retry := state.Active.CheckRetry
			if len(retry.FailedRuns) != 1 || retry.FailedRuns[0].Name != "ci" {
				t.Fatalf("effective failures=%+v", retry.FailedRuns)
			}
			cfg := queue.Config{ChecksRetryFailed: 1}
			var request queue.RecordCheckRerun
			for _, action := range queue.Plan(cfg, state, epoch) {
				if a, ok := action.(queue.RecordCheckRerun); ok {
					request = a
				}
			}
			if len(request.Runs) != 1 || request.Runs[0].Name != "ci" {
				t.Fatalf("retry=%+v", request)
			}
			state = queue.Apply(state, queue.CheckRerunRequested{BatchID: "b1", TestedSHA: "tested", Generation: request.Generation, Runs: request.Runs, At: epoch})
			state = queue.Apply(state, queue.CheckRerunDispatched{BatchID: "b1", TestedSHA: "tested", Generation: request.Generation})
			// Only ci gets a new identity. Accepted linux and optional fallback retain IDs.
			checks[0].ID = 4
			checks[0].State = forge.Success
			f.SetCheckRuns(repo, "tested", checks)
			x.CheckRetry = state.Active.CheckRetry
			must(t, x.Execute(t.Context(), queue.RefreshChecks{BatchID: "b1", TestedSHA: "tested"}, emit))
			if !state.ChecksPassed("tested") || state.Active.CheckRetry.RerunState != "passed" {
				t.Fatalf("unchanged optional check blocked rerun: %+v", state.Active.CheckRetry)
			}
		})
	}
}
