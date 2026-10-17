package serve

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCheckRetryStatusIncludesPassedFlakyIdentity(t *testing.T) {
	got := checkRetryStatus(&queue.CheckRetryState{
		Dispatched: true,
		RerunState: "passed",
		FailedRuns: []queue.CheckRun{{WorkflowRunID: 700, WorkflowRunURL: "https://github.test/acme/project/actions/runs/700", URL: "https://github.test/acme/project/actions/runs/700/job/701", ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}},
	})
	for _, want := range []string{"check retry passed", "1 flaky original", "ci/app:ci#7.1", "workflow=700", "https://github.test/acme/project/actions/runs/700", "/job/701"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status=%q missing %q", got, want)
		}
	}
}

func TestChainSummaryRootAndMixedWorkerFailure(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig+"    speculation:\n      max_depth: 4\n")
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	assertSummary := func(want string) {
		t.Helper()
		statuses := f.Statuses(repo, 1)
		if len(statuses) == 0 {
			t.Fatal("missing PR status")
		}
		for _, status := range statuses {
			if status.State != forge.Testing || !strings.HasPrefix(status.Summary, want+"\n\nQueue: window: 4; batch size: 1; p̂: n/a (0 samples); chain: b1(testing); gate: checks") {
				t.Fatalf("status=%+v, want testing with summary %q", status, want)
			}
		}
	}
	assertSummary("Batch b1; chain position 1; 0 batches ahead; check retry observed")
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	b := state.Active
	_, err = s.store.Append(t.Context(), w.id, []queue.Event{
		queue.ChecksObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Terminal: true, Timeout: b.CheckRetry.Timeout, OriginalDeadline: b.CheckRetry.OriginalDeadline, Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}},
		queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: false},
	}, "")
	must(t, err)
	must(t, s.report(t.Context(), w))
	assertSummary("Batch b1; chain position 1; 0 batches ahead; check retry failed; provisional failure: checks-failed")
	for _, summary := range f.Statuses(repo, 1) {
		for _, want := range []string{"CI run ledger:", "ci/app:ci#1.1=failure"} {
			if !strings.Contains(summary.Summary, want) {
				t.Fatalf("missing %q in %s", want, summary.Summary)
			}
		}
	}
}
