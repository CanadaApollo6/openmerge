package store

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

var epoch = time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
var config = queue.Config{MergeMethod: queue.Squash, CheckTimeout: time.Minute, ResolveAfter: time.Minute}

func eventSamples() []queue.Event {
	return []queue.Event{
		queue.BatchStarted{BatchID: "b1", BaseSHA: "base", PRs: []queue.BatchPR{{PR: 2, HeadSHA: "head"}}, At: epoch},
		queue.BatchFailed{BatchID: "b1", Reason: queue.ChecksTimeout},
		queue.PRObserved{PR: 2, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.BaseMoved{SHA: "base"}, queue.ChecksCompleted{TestedSHA: "tested", Success: true},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: epoch}, queue.PRClosed{PR: 2},
		queue.EnqueueRequested{PR: 2, At: epoch}, queue.DequeueRequested{PR: 2}, queue.ResumeRequested{},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: epoch, Method: queue.Squash},
		queue.TestRefBuildFailed{BatchID: "b1", Conflict: true, At: epoch},
		queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: epoch},
		queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"},
		queue.LandingAbandoned{BatchID: "b1", NewFence: "next"},
		queue.Dequeued{PR: 2, AdmissionID: 3, Reason: queue.Requested, BatchID: "b1"},
		queue.ActionCompleted{Key: "effect"},
		queue.ActionPoisoned{Key: "batch/b1/land", Reason: "invalid payload"},
	}
}

func seedEvents() []queue.Event {
	return []queue.Event{
		queue.BaseMoved{SHA: "base"},
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 1, At: epoch},
	}
}

func fold(state queue.State, events []queue.Event) queue.State {
	for _, event := range events {
		state = queue.Apply(state, event)
	}
	return state
}

func TestOnlyTransactionScopedAdvisoryLocks(t *testing.T) {
	files, err := filepath.Glob("*.go")
	must(t, err)
	sql, err := filepath.Glob("migrations/*.sql")
	must(t, err)
	files = append(files, sql...)
	sessionLock := regexp.MustCompile(`(?i)pg_(?:try_)?advisory_(?:lock|unlock)(?:_shared|_all)?\s*\(`)
	for _, file := range files {
		if regexp.MustCompile(`_test\.go$`).MatchString(file) {
			continue
		}
		data, err := os.ReadFile(file)
		must(t, err)
		if sessionLock.Match(data) {
			t.Errorf("session-level advisory lock in %s", file)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
