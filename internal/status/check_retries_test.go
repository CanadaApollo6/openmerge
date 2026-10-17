package status

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCheckRetryProjectionAndEvidence(t *testing.T) {
	timeout := time.Minute
	original := []queue.CheckRun{{ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}
	events := append(testingEvents(),
		queue.ChecksObserved{
			BatchID: "b1", TestedSHA: "tested", Runs: original, Terminal: true,
			OriginalDeadline: at.Add(timeout), Timeout: timeout,
		},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: at},
		queue.CheckRerunRequested{
			BatchID: "b1", TestedSHA: "tested", Generation: 0, Runs: original,
			OriginalDeadline: at.Add(timeout), At: at.Add(10 * time.Second),
			Deadline: at.Add(70 * time.Second), Timeout: timeout,
		},
		queue.CheckRerunDispatched{BatchID: "b1", TestedSHA: "tested", Generation: 0, Detail: "provider accepted retry"},
		queue.ChecksObserved{
			BatchID: "b1", TestedSHA: "tested",
			Runs:     []queue.CheckRun{{ID: 7, Attempt: 2, Name: "ci", Source: "app:ci", State: "success"}},
			Terminal: true, Success: true, OriginalDeadline: at.Add(timeout), Timeout: timeout,
		},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: at.Add(20 * time.Second)},
	)
	q, _, why := Project(journal(events...), 1, 20, nil)
	if q.ActiveBatch == nil || q.ActiveBatch.CheckRetry == nil {
		t.Fatalf("retry state missing: %+v", q.ActiveBatch)
	}
	retry := q.ActiveBatch.CheckRetry
	if retry.RerunState != "passed" || !retry.Requested || !retry.Dispatched || retry.Detail != "provider accepted retry" || retry.Generation != 0 {
		t.Fatalf("retry projection=%+v", retry)
	}
	if !retry.OriginalDeadline.Equal(at.Add(timeout)) || !retry.Deadline.Equal(at.Add(70*time.Second)) || !retry.RequestedAt.Equal(at.Add(10*time.Second)) {
		t.Fatalf("retry deadlines=%+v", retry)
	}
	if len(retry.FailedRuns) != 1 || retry.FailedRuns[0] != original[0] || len(retry.ObservedRuns) != 1 || retry.ObservedRuns[0].Attempt != 2 {
		t.Fatalf("retry runs=%+v", retry)
	}
	data, err := json.Marshal(q.ActiveBatch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"check_retry"`) || !strings.Contains(string(data), `"rerun_state":"passed"`) {
		t.Fatalf("retry missing from API JSON: %s", data)
	}
	var summaries []string
	for _, event := range why.Events {
		summaries = append(summaries, event.Summary)
	}
	joined := strings.Join(summaries, "\n")
	for _, want := range []string{
		"retry-aware terminal CI result",
		"Requested bounded retry",
		"Dispatched bounded check retry: provider accepted retry",
		"ci/app:ci#7.1=failure",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence summaries=%q missing %q", joined, want)
		}
	}

	landed := append(append([]queue.Event(nil), events...),
		queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: at.Add(time.Minute)},
		queue.LandingCleanupPolicyRecorded{BatchID: "b1", AttemptID: "attempt", DeleteHeadBranch: true},
		queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"},
	)
	q, _, why = Project(journal(landed...), 1, 50, nil)
	if q.ActiveBatch != nil {
		t.Fatalf("landed batch remained active: %+v", q.ActiveBatch)
	}
	foundRuns := false
	for _, event := range why.Events {
		if event.Type == "checks_observed" && len(event.Runs) == 1 && event.Runs[0].ID == 7 {
			foundRuns = true
		}
	}
	if !foundRuns {
		t.Fatalf("historical retry identities missing after landing: %+v", why.Events)
	}
}

func TestRetryDisabledBatchJSONOmitsRetryState(t *testing.T) {
	q, _, _ := Project(journal(testingEvents()...), 0, 0, nil)
	data, err := json.Marshal(q.ActiveBatch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"check_retry"`) {
		t.Fatalf("default-off API gained retry state: %s", data)
	}
}
