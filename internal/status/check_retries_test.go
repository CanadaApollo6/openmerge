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
	original := []queue.CheckRun{{WorkflowRunID: 700, WorkflowRunURL: "https://github.test/acme/project/actions/runs/700", URL: "https://github.test/acme/project/actions/runs/700/job/701", ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}
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
	if !strings.Contains(string(data), `"check_retry"`) || !strings.Contains(string(data), `"rerun_state":"passed"`) || !strings.Contains(string(data), `"workflow_run_id":700`) || !strings.Contains(string(data), `/job/701`) {
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
		"workflow=700 https://github.test/acme/project/actions/runs/700",
		"job=https://github.test/acme/project/actions/runs/700/job/701",
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

func TestUnconfirmedRetryExplainedInStatusAndWhy(t *testing.T) {
	for _, dispatched := range []bool{false, true} {
		runs := []queue.CheckRun{{WorkflowRunID: 700, ID: 7, Attempt: 1, Name: "ci", Source: "app:15368", State: "failure"}}
		events := append(testingEvents(),
			queue.ChecksObserved{BatchID: "b1", TestedSHA: "tested", Runs: runs, Terminal: true, OriginalDeadline: at.Add(time.Minute), Timeout: time.Minute},
			queue.CheckRerunRequested{BatchID: "b1", TestedSHA: "tested", Runs: runs, OriginalDeadline: at.Add(time.Minute), At: at, Deadline: at.Add(time.Minute), Timeout: time.Minute},
			queue.CheckRerunDispatchProgress{BatchID: "b1", TestedSHA: "tested", Target: queue.CheckRerunTarget{WorkflowRunID: 700, Attempt: 1, At: at}},
		)
		// Even aggregate completion must not hide a target lacking acceptance.
		if dispatched {
			events = append(events, queue.CheckRerunDispatched{BatchID: "b1", TestedSHA: "tested"})
		}
		events = append(events, queue.ChecksFailureConfirmed{BatchID: "b1", TestedSHA: "tested", Reason: queue.ChecksTimeout})
		q, entry, why := Project(journal(events...), 1, 50, nil)
		if entry == nil || !strings.Contains(entry.Detail, "rerun dispatch never confirmed") || q.ActiveBatch == nil || !strings.Contains(q.ActiveBatch.CheckRetry.Detail, "rerun dispatch never confirmed") {
			t.Fatalf("pending status=%+v retry=%+v", entry, q.ActiveBatch)
		}
		found := false
		for _, e := range why.Events {
			if e.Type == "checks_failure_confirmed" && strings.Contains(e.Summary, "rerun dispatch never confirmed") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing why explanation: %+v", why)
		}
		events = append(events, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.ChecksTimeout, BatchID: "b1"})
		_, entry, _ = Project(journal(events...), 1, 50, nil)
		if entry == nil || !strings.Contains(entry.Detail, "rerun dispatch never confirmed") {
			t.Fatalf("terminal status=%+v", entry)
		}
	}
}
