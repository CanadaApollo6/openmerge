package executor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestRetryRefreshCapturesBoundedIdentitiesAndOriginalDeadline(t *testing.T) {
	h := setup(t)
	h.x.ChecksRetryFailed = 1
	h.x.CheckTimeout = 2 * time.Minute
	h.x.StartedTestingAt = epoch
	sha := h.tested(t)
	ref := h.buildAction().Ref
	h.f.SetCheckRuns(repo, sha, []forge.CheckRun{
		{ID: 10, Attempt: 2, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: ref},
		{ID: 10, Attempt: 1, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: ref},
		{ID: 11, Attempt: 1, Name: "lint", Source: "app:lint", State: forge.Success, HeadBranch: ref},
		{ID: 12, Attempt: 1, Name: "unrelated", Source: "app:other", State: forge.Failure, HeadBranch: ref},
		{ID: 99, Attempt: 1, Name: "ci", Source: "foreign", State: forge.Success, HeadBranch: "other-ref"},
	})
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, record(&events)))
	want := []queue.Event{
		queue.ChecksObserved{
			BatchID: "batch", TestedSHA: sha,
			Runs: []queue.CheckRun{
				{ID: 10, Attempt: 2, Name: "ci", Source: "app:ci", State: "failure"},
				{ID: 11, Attempt: 1, Name: "lint", Source: "app:lint", State: "success"},
			},
			Terminal: true, Success: false, OriginalDeadline: epoch.Add(2 * time.Minute), Timeout: 2 * time.Minute,
		},
		queue.ChecksRefreshed{BatchID: "batch", TestedSHA: sha, At: epoch},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestRetryRefreshCanonicalizesZeroAttempt(t *testing.T) {
	h := setup(t)
	h.x.ChecksRetryFailed = 1
	sha := h.tested(t)
	ref := h.buildAction().Ref
	h.f.SetCheckRuns(repo, sha, []forge.CheckRun{
		{ID: 10, Attempt: 1, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: ref},
		{ID: 10, Attempt: 0, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: ref},
	})
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, record(&events)))
	observed, ok := events[0].(queue.ChecksObserved)
	if !ok || !reflect.DeepEqual(observed.Runs, []queue.CheckRun{{ID: 10, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}) {
		t.Fatalf("canonical retry observation=%#v", events)
	}
}

func TestRetryCheckPolicyReceivesNewestProducerAttempts(t *testing.T) {
	h := setup(t)
	h.x.ChecksRetryFailed = 1
	sha := h.tested(t)
	ref := h.buildAction().Ref
	var received []forge.CheckRun
	h.x.CheckPolicy = func(checks []forge.CheckRun) (bool, bool, error) {
		received = append(received, checks...)
		return true, true, nil
	}
	h.f.SetCheckRuns(repo, sha, []forge.CheckRun{
		{ID: 10, Attempt: 2, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: ref},
		{ID: 10, Attempt: 1, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: ref},
		{ID: 11, Attempt: 1, Name: "lint", Source: "app:lint", State: forge.Success, HeadBranch: ref},
	})
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), queue.RefreshChecks{BatchID: "batch", TestedSHA: sha}, record(&events)))
	if !reflect.DeepEqual(received, []forge.CheckRun{
		{ID: 10, Attempt: 2, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: ref},
		{ID: 11, Attempt: 1, Name: "lint", Source: "app:lint", State: forge.Success, HeadBranch: ref},
	}) {
		t.Fatalf("policy checks=%#v", received)
	}
}

func TestRetryPolicyErrorEmitsNothing(t *testing.T) {
	for _, operation := range []string{"refresh", "land"} {
		t.Run(operation, func(t *testing.T) {
			h := setup(t)
			h.x.ChecksRetryFailed = 1
			h.x.CheckPolicy = func([]forge.CheckRun) (bool, bool, error) { return false, false, errIO }
			sha := h.tested(t)
			h.f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", State: forge.Success}, {Name: "lint", State: forge.Success}})
			action := queue.Action(queue.RefreshChecks{BatchID: "batch", TestedSHA: sha})
			if operation == "land" {
				action = queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: sha, FenceToken: h.fence, Method: queue.Merge, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}
			}
			var events []queue.Event
			err := h.x.Execute(context.Background(), action, record(&events))
			if !errors.Is(err, errIO) || len(events) != 0 {
				t.Fatalf("error=%v events=%#v", err, events)
			}
		})
	}
}

func TestRetryLandRejectsStaleSuccessUntilEveryFailedRunIsNewer(t *testing.T) {
	h := setup(t)
	h.x.ChecksRetryFailed = 1
	h.x.CheckTimeout = time.Minute
	h.x.StartedTestingAt = epoch
	sha := h.tested(t)
	ref := h.buildAction().Ref
	h.x.CheckRetry = &queue.CheckRetryState{
		Requested:  true,
		Dispatched: true,
		RerunState: "passed",
		FailedRuns: []queue.CheckRun{
			{ID: 7, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"},
			{ID: 8, Attempt: 1, Name: "lint", Source: "app:lint", State: "failure"},
		},
	}
	h.f.SetCheckRuns(repo, sha, []forge.CheckRun{
		{ID: 7, Attempt: 2, Name: "ci", Source: "app:ci", State: forge.Success, HeadBranch: ref},
		{ID: 8, Attempt: 1, Name: "lint", Source: "app:lint", State: forge.Success, HeadBranch: ref},
	})
	land := queue.Land{BatchID: "batch", ExpectedBaseSHA: h.base, TestedSHA: sha, FenceToken: h.fence, Method: queue.Merge, PRs: []queue.BatchPR{{PR: 1, HeadSHA: h.prs[0].HeadSHA}}}
	var events []queue.Event
	err := h.x.Execute(context.Background(), land, record(&events))
	if !errors.Is(err, executor.ErrChecksNotSuccessful) || h.read(t, "main") != h.base {
		t.Fatalf("stale successful read authorized landing: err=%v base=%s", err, h.read(t, "main"))
	}
	if len(events) != 2 {
		t.Fatalf("events=%#v", events)
	}
	observed, ok := events[0].(queue.ChecksObserved)
	if !ok || observed.Terminal || observed.Success || !reflect.DeepEqual(observed.Runs, []queue.CheckRun{
		{ID: 7, Attempt: 2, Name: "ci", Source: "app:ci", State: "success"},
		{ID: 8, Attempt: 1, Name: "lint", Source: "app:lint", State: "success"},
	}) {
		t.Fatalf("stale observation=%#v", events[0])
	}
	if _, ok := events[1].(queue.ChecksRefreshed); !ok {
		t.Fatalf("missing refresh watermark: %#v", events)
	}
}

func TestRerunActionsUseExactRunsAndReportCapabilityResult(t *testing.T) {
	h := setup(t)
	sha := h.tested(t)
	ref := h.buildAction().Ref
	h.f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 40, Attempt: 1, Name: "ci", Source: "app:ci", State: forge.Failure, HeadBranch: ref}})
	action := queue.RerunChecks{BatchID: "batch", TestedSHA: sha, TestRef: ref, Generation: 3, Runs: []queue.CheckRun{{ID: 40, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}}
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), action, record(&events)))
	if !reflect.DeepEqual(events, []queue.Event{queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: sha, Generation: 3, Detail: "rerun requested"}}) {
		t.Fatalf("dispatch events=%#v", events)
	}
	runs, err := h.f.CheckRuns(context.Background(), repo, sha)
	must(t, err)
	if len(runs) != 2 || runs[1].ID <= runs[0].ID || runs[1].Attempt <= runs[0].Attempt || runs[1].HeadBranch != ref {
		t.Fatalf("rerun runs=%#v", runs)
	}
	h.f.FailNext("RerunFailedChecks", errIO)
	events = nil
	if err := h.x.Execute(context.Background(), action, record(&events)); !errors.Is(err, errIO) || len(events) != 0 {
		t.Fatalf("transient rerun result err=%v events=%#v", err, events)
	}
}

func TestRecordAndConfirmCheckRetryActions(t *testing.T) {
	h := setup(t)
	h.x.Now = func() time.Time { return epoch.Add(5 * time.Second) }
	runs := []queue.CheckRun{{ID: 4, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}
	recordAction := queue.RecordCheckRerun{BatchID: "batch", TestedSHA: "sha", Generation: 4, Runs: runs, OriginalDeadline: epoch.Add(time.Minute), Timeout: 2 * time.Minute}
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), recordAction, record(&events)))
	want := queue.CheckRerunRequested{BatchID: "batch", TestedSHA: "sha", Generation: 4, Runs: runs, OriginalDeadline: epoch.Add(time.Minute), At: epoch.Add(5 * time.Second), Deadline: epoch.Add(125 * time.Second), Timeout: 2 * time.Minute}
	if !reflect.DeepEqual(events, []queue.Event{want}) {
		t.Fatalf("request event=%#v want=%#v", events, want)
	}
	events = nil
	recordAction.Timeout = 0
	recordAction.OriginalDeadline = time.Time{}
	must(t, h.x.Execute(t.Context(), recordAction, record(&events)))
	bounded := events[0].(queue.CheckRerunRequested)
	if !bounded.Deadline.Equal(epoch.Add(5*time.Second+queue.DefaultCheckTimeout)) || bounded.Timeout != 0 || !bounded.OriginalDeadline.IsZero() {
		t.Fatalf("unbounded rerun with original timer disabled: %+v", bounded)
	}
	must(t, bounded.Validate())

	events = nil
	confirm := queue.ConfirmCheckFailure{BatchID: "batch", TestedSHA: "sha", Generation: 4, Reason: queue.ChecksFailed}
	must(t, h.x.Execute(context.Background(), confirm, record(&events)))
	if !reflect.DeepEqual(events, []queue.Event{queue.ChecksFailureConfirmed{BatchID: "batch", TestedSHA: "sha", Generation: 4, Reason: queue.ChecksFailed}}) {
		t.Fatalf("confirm event=%#v", events)
	}
}

type driverWithoutRerunner struct{ forge.Driver }

func TestRerunWithoutCapabilityIsUnsupported(t *testing.T) {
	h := setup(t)
	h.x.Driver = driverWithoutRerunner{Driver: h.f}
	action := queue.RerunChecks{BatchID: "batch", TestedSHA: "sha", TestRef: "ref", Generation: 2, Runs: []queue.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "app:ci", State: "failure"}}}
	var events []queue.Event
	must(t, h.x.Execute(context.Background(), action, record(&events)))
	want := queue.CheckRerunDispatched{BatchID: "batch", TestedSHA: "sha", Generation: 2, Unsupported: true, Detail: "forge does not support failed-check reruns"}
	if !reflect.DeepEqual(events, []queue.Event{want}) {
		t.Fatalf("unsupported event=%#v want=%#v", events, want)
	}
}
