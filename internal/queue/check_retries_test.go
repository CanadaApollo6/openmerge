package queue

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func retryTesting(t *testing.T, size int) (Config, State) {
	t.Helper()
	cfg := batchConfig(size)
	cfg.ChecksRetryFailed = 1
	cfg.CheckTimeout = time.Minute
	s := startPlanned(t, cfg, batchQueue(size))
	return cfg, foldCheckEvents(t, s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested", Method: Merge, At: epoch})
}
func retryObservation(cfg Config, s State, id uint64, state string) ChecksObserved {
	return ChecksObserved{BatchID: s.Active.ID, TestedSHA: s.Active.TestedSHA, Runs: []CheckRun{{ID: id, Attempt: 1, Name: "ci", Source: "app:1", State: state}}, Terminal: state != "pending", Success: state == "success", OriginalDeadline: s.Active.StartedTestingAt.Add(cfg.CheckTimeout), Timeout: cfg.CheckTimeout}
}
func requestRetry(t *testing.T, cfg Config, s State, at time.Time) State {
	t.Helper()
	a := onlyAction[RecordCheckRerun](t, Plan(cfg, s, at))
	timeout := a.Timeout
	if timeout == 0 {
		timeout = DefaultCheckTimeout
	}
	e := CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, OriginalDeadline: a.OriginalDeadline, At: at, Deadline: at.Add(timeout), Timeout: a.Timeout}
	if !CanStart(cfg, s, a, e) {
		t.Fatal("current request declined")
	}
	return foldCheckEvents(t, s, e)
}
func dispatchRetry(t *testing.T, cfg Config, s State, at time.Time, unsupported bool) State {
	t.Helper()
	a := onlyAction[RerunChecks](t, Plan(cfg, s, at))
	return foldCheckEvents(t, s, CheckRerunDispatched{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Unsupported: unsupported, Detail: "capability result"})
}

func TestCheckRetryPassDeduplicatesOriginalAndKeepsFlakyRun(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	failure := retryObservation(cfg, s, 7, "failure")
	s = foldCheckEvents(t, s, failure)
	before := cloneState(s)
	s = requestRetry(t, cfg, s, epoch.Add(20*time.Second))
	if !reflect.DeepEqual(before.Active.CheckRetry.ObservedRuns, failure.Runs) || before.Active.CheckRetry.Requested {
		t.Fatal("fold mutated the earlier state")
	}
	// Duplicate durable requests cannot move either recorded deadline.
	original := *s.Active.CheckRetry
	s = foldCheckEvents(t, s, CheckRerunRequested{BatchID: s.Active.ID, TestedSHA: "tested", Generation: s.Active.checksGeneration, Runs: failure.Runs, OriginalDeadline: original.OriginalDeadline, At: epoch.Add(30 * time.Second), Deadline: epoch.Add(90 * time.Second), Timeout: time.Minute})
	if !reflect.DeepEqual(original, *s.Active.CheckRetry) {
		t.Fatal("duplicate request changed retry")
	}
	s = dispatchRetry(t, cfg, s, epoch.Add(20*time.Second), false)
	for range 3 {
		s = foldCheckEvents(t, s, failure)
	}
	onlyAction[RefreshChecks](t, Plan(cfg, s, epoch.Add(21*time.Second)))
	pass := retryObservation(cfg, s, 8, "success")
	s = foldCheckEvents(t, s, pass)
	land := onlyAction[Land](t, Plan(cfg, s, epoch.Add(time.Hour)))
	s = foldCheckEvents(t, s, failure)
	if s.Active.CheckRetry.RerunState != "passed" || !slices.Equal(s.Active.CheckRetry.FailedRuns, failure.Runs) || !s.ChecksPassed("tested") {
		t.Fatal("flaky run lost or duplicate revoked rerun pass")
	}
	onlyAction[Land](t, Plan(cfg, s, epoch.Add(time.Hour)))
	// Reopening uses a new landing generation, without restoring the retry.
	s = foldCheckEvents(t, s, ChecksReopened{TestedSHA: "tested", At: epoch.Add(time.Hour)})
	s = foldCheckEvents(t, s, pass)
	next := onlyAction[Land](t, Plan(cfg, s, epoch.Add(time.Hour)))
	if next.Key() == land.Key() || CanStart(cfg, s, land, LandingStarted{BatchID: land.BatchID, AttemptID: "old", FenceToken: land.FenceToken, At: epoch.Add(time.Hour)}) {
		t.Fatal("reopen revived an old landing")
	}
	if !s.Active.CheckRetry.Requested || !s.Active.CheckRetry.Deadline.Equal(original.Deadline) {
		t.Fatal("reopen reset retry")
	}
}

func TestCheckRetryFailureIsConfirmedOnceAndDiagnosed(t *testing.T) {
	for _, size := range []int{1, 4} {
		cfg, s := retryTesting(t, size)
		s = foldCheckEvents(t, s, retryObservation(cfg, s, 7, "failure"))
		s = requestRetry(t, cfg, s, epoch.Add(time.Second))
		s = dispatchRetry(t, cfg, s, epoch.Add(time.Second), false)
		s = foldCheckEvents(t, s, retryObservation(cfg, s, 8, "failure"))
		a := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, epoch.Add(2*time.Second)))
		e := ChecksFailureConfirmed(a)
		s = foldCheckEvents(t, s, e)
		if size == 1 {
			if !s.Active.CheckRetry.Confirmed || s.PendingDequeue(1) != ChecksFailed {
				t.Fatal("singleton was not confirmed")
			}
			s = foldCheckEvents(t, s, ChecksReopened{TestedSHA: "tested", At: epoch}, ChecksCompleted{TestedSHA: "tested", Success: true}, retryObservation(cfg, s, 9, "success"), e)
			if s.ChecksPassed("tested") || s.PendingDequeue(1) != ChecksFailed {
				t.Fatal("confirmed failure revived")
			}
		} else {
			if s.Active != nil || len(s.pending) != 2 || len(s.pending[0].prs) != 2 || len(s.pending[1].prs) != 2 {
				t.Fatal("confirmed failure did not split")
			}
			duplicate := foldCheckEvents(t, s, e)
			if !reflect.DeepEqual(s.pending, duplicate.pending) {
				t.Fatal("confirmation split twice")
			}
		}
	}
}

func TestCheckRetryDeadlinesRequireFreshEvidence(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 7, "failure"))
	s = requestRetry(t, cfg, s, epoch.Add(50*time.Second))
	s = dispatchRetry(t, cfg, s, epoch.Add(50*time.Second), false)
	deadline := epoch.Add(110 * time.Second)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 8, "pending"), ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: epoch.Add(70 * time.Second)})
	// The original deadline has passed; the retry has its own full minute.
	for _, a := range Plan(cfg, s, epoch.Add(71*time.Second)) {
		if _, ok := a.(ConfirmCheckFailure); ok {
			t.Fatal("original deadline timed out retry")
		}
	}
	// A post-deadline clock alone cannot confirm, nor can a refresh from before it.
	onlyAction[RefreshChecks](t, Plan(cfg, s, deadline.Add(time.Second)))
	s = foldCheckEvents(t, s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: deadline})
	onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, deadline.Add(time.Second)))
	s = foldCheckEvents(t, s, ChecksReopened{TestedSHA: "tested", At: epoch.Add(-time.Hour)})
	onlyAction[RefreshChecks](t, Plan(cfg, s, deadline.Add(time.Second)))
	s = foldCheckEvents(t, s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: epoch})
	onlyAction[RefreshChecks](t, Plan(cfg, s, deadline.Add(time.Second)))
	s = foldCheckEvents(t, s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: deadline})
	a := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, deadline.Add(time.Second)))
	if a.Reason != ChecksTimeout {
		t.Fatal("retry timeout was retried")
	}
}

func TestCheckRetryOriginalTimeoutUnsupportedAndLegacy(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	pending := retryObservation(cfg, s, 7, "pending")
	s = foldCheckEvents(t, s, pending, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: epoch.Add(time.Minute)})
	a := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, epoch.Add(time.Minute+time.Second)))
	if a.Reason != ChecksTimeout {
		t.Fatal("original timeout was retried")
	}
	cfg, s = retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 7, "failure"))
	s = requestRetry(t, cfg, s, epoch.Add(time.Second))
	s = dispatchRetry(t, cfg, s, epoch.Add(time.Second), true)
	a = onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, epoch.Add(time.Second)))
	if a.Reason != ChecksFailed || s.Active.CheckRetry.RerunState != "unsupported" {
		t.Fatal("unsupported was not confirmed")
	}
	// Historical failed ChecksCompleted is immediate even with retries configured.
	_, s = retryTesting(t, 1)
	s = foldCheckEvents(t, s, ChecksCompleted{TestedSHA: "tested", Success: false})
	if s.PendingDequeue(1) != ChecksFailed || s.Active.CheckRetry != nil {
		t.Fatal("legacy failed check gained a retry")
	}
}

func TestCheckRetryRequiresEveryFailedProducerAndNewAttempt(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	failed := retryObservation(cfg, s, 7, "failure")
	failed.Runs = append(failed.Runs, CheckRun{ID: 9, Attempt: 1, Name: "lint", Source: "app:2", State: "failure"})
	s = foldCheckEvents(t, s, failed)
	s = requestRetry(t, cfg, s, epoch.Add(time.Second))
	s = dispatchRetry(t, cfg, s, epoch.Add(time.Second), false)
	for _, bad := range [][]CheckRun{
		{{ID: 8, Name: "ci", Source: "app:other", State: "success"}, {ID: 10, Name: "lint", Source: "app:2", State: "success"}},
		{{ID: 7, Attempt: 1, Name: "ci", Source: "app:1", State: "success"}, {ID: 10, Name: "lint", Source: "app:2", State: "success"}},
	} {
		observed := failed
		observed.Success = true
		observed.Runs = bad
		s = foldCheckEvents(t, s, observed)
		if s.ChecksPassed("tested") {
			t.Fatal("old run or different producer passed rerun")
		}
	}
	passed := failed
	passed.Success = true
	passed.Runs = slices.Clone(failed.Runs)
	for i := range passed.Runs {
		passed.Runs[i].Attempt++
		passed.Runs[i].State = "success"
	}
	// The rejected aggregate still retained lint's newer identity from the
	// partial observation; a coherent pass must not move it backwards.
	passed.Runs[1].ID = 10
	s = foldCheckEvents(t, s, passed)
	onlyAction[Land](t, Plan(cfg, s, epoch.Add(2*time.Second)))
	// The exported compact retry state is sufficient to retain both timer origins
	// and run identities in a future storage snapshot; it has no event history.
	data, err := json.Marshal(s.Active.CheckRetry)
	if err != nil {
		t.Fatal(err)
	}
	var restored CheckRetryState
	if err = json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(restored, *s.Active.CheckRetry) {
		t.Fatalf("snapshot lost retry state: %s %v", data, err)
	}
}

func TestCheckRetryNextWakeAndNoTimeout(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 1, "failure"))
	s = requestRetry(t, cfg, s, epoch.Add(50*time.Second))
	s = dispatchRetry(t, cfg, s, epoch.Add(50*time.Second), false)
	if at, ok := NextWake(cfg, s, epoch.Add(50*time.Second)); !ok || !at.Equal(epoch.Add(110*time.Second+time.Nanosecond)) {
		t.Fatalf("rerun wake: %v %v", at, ok)
	}
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 2, "success"))
	if at, ok := NextWake(cfg, s, epoch); ok {
		t.Fatalf("passing batch has timeout wake %v", at)
	}
	cfg, s = retryTesting(t, 1)
	cfg.CheckTimeout = 0
	failure := retryObservation(cfg, s, 1, "failure")
	failure.OriginalDeadline = time.Time{}
	s = foldCheckEvents(t, s, failure)
	a := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, CheckRerunRequested{BatchID: a.BatchID, TestedSHA: a.TestedSHA, Generation: a.Generation, Runs: a.Runs, At: epoch, Deadline: epoch.Add(DefaultCheckTimeout), Timeout: 0})
	if !s.Active.CheckRetry.OriginalDeadline.IsZero() || !s.Active.CheckRetry.Deadline.Equal(epoch.Add(DefaultCheckTimeout)) {
		t.Fatal("rerun did not gain its bounded default deadline")
	}
	if at, ok := NextWake(cfg, s, epoch); !ok || at.After(epoch.Add(DefaultCheckTimeout+time.Nanosecond)) {
		t.Fatalf("no bounded wake: %v %v", at, ok)
	}
	// An unconfirmed dispatch still requires a fresh post-deadline observation.
	now := epoch.Add(DefaultCheckTimeout + time.Second)
	onlyAction[RefreshChecks](t, Plan(cfg, s, now))
	s = foldCheckEvents(t, s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: now})
	confirm := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, now))
	if confirm.Reason != ChecksTimeout {
		t.Fatalf("confirmation=%+v", confirm)
	}
	s = foldCheckEvents(t, s, ChecksFailureConfirmed(confirm))
	if detail := s.DequeueDetail(1, ChecksTimeout); !strings.Contains(detail, "rerun dispatch never confirmed") {
		t.Fatal(detail)
	}
}

func TestTerminalFailureRetryCannotRevivePreflightedLanding(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 1, "success"))
	old := onlyAction[Land](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 2, "failure"))
	s = requestRetry(t, cfg, s, epoch.Add(time.Second))
	s = dispatchRetry(t, cfg, s, epoch.Add(time.Second), false)
	// Both pending notifications may be lost: the terminal failure itself must
	// invalidate the old landing generation before a terminal rerun pass arrives.
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 3, "success"))
	fresh := onlyAction[Land](t, Plan(cfg, s, epoch.Add(2*time.Second)))
	if fresh.Key() == old.Key() || CanStart(cfg, s, old, LandingStarted{BatchID: old.BatchID, AttemptID: "stale", FenceToken: old.FenceToken, At: epoch.Add(2 * time.Second)}) {
		t.Fatal("terminal failure and rerun pass revived old landing")
	}
}

func TestNewFailureBeforeRetryRequestReplacesOnlyUnspentSubject(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	first := retryObservation(cfg, s, 1, "failure")
	s = foldCheckEvents(t, s, first)
	old := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 2, "failure"))
	next := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	if next.Key() == old.Key() || next.Runs[0].ID != 2 {
		t.Fatal("new failure reused old request payload or key")
	}
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 3, "success"))
	onlyAction[Land](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 4, "failure"))
	next = onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	if next.Runs[0].ID != 4 {
		t.Fatal("external recovery retained obsolete failed run")
	}
	s = requestRetry(t, cfg, s, epoch)
	saved := slices.Clone(s.Active.CheckRetry.FailedRuns)
	s = foldCheckEvents(t, s, first)
	if !slices.Equal(saved, s.Active.CheckRetry.FailedRuns) {
		t.Fatal("old duplicate replaced spent retry subject")
	}
}

func TestDelayedOriginalObservationCannotReplaceLatestUnspentRetry(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	old := retryObservation(cfg, s, 1, "failure")
	newer := retryObservation(cfg, s, 2, "failure")
	s = foldCheckEvents(t, s, old, newer)
	planned := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	// Missing results and stale pending reads must preserve identity ordering too.
	pending := old
	pending.Terminal = false
	pending.Runs = nil
	s = foldCheckEvents(t, s, pending, old)
	pending.Runs = slices.Clone(old.Runs)
	pending.Runs[0].State = "pending"
	s = foldCheckEvents(t, s, pending, old)
	next := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
	if next.Runs[0].ID != 2 {
		t.Fatal("delayed original failure replaced newer retry subject")
	}
	// Reopens may change the generation, but the target can never regress.
	if planned.Runs[0].ID != next.Runs[0].ID {
		t.Fatal("retry target regressed")
	}
	s = requestRetry(t, cfg, s, epoch)
	if s.Active.CheckRetry.FailedRuns[0].ID != 2 {
		t.Fatal("request consumed retry on stale run")
	}
}

func TestDelayedFailureBeforeRequestCannotUndoExternalNewerPass(t *testing.T) {
	cfg, s := retryTesting(t, 1)
	original := retryObservation(cfg, s, 1, "failure")
	s = foldCheckEvents(t, s, original, retryObservation(cfg, s, 2, "success"))
	s = foldCheckEvents(t, s, original)
	onlyAction[Land](t, Plan(cfg, s, epoch))
	pending := original
	pending.Terminal = false
	pending.Runs = nil
	s = foldCheckEvents(t, s, pending, original)
	if s.Active.CheckRetry.ObservedRuns[0].ID != 2 {
		t.Fatal("missing pending read erased newer identity")
	}
	onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
	s = foldCheckEvents(t, s, retryObservation(cfg, s, 2, "success"))
	onlyAction[Land](t, Plan(cfg, s, epoch))
}

func TestMixedStaleObservationRetainsFreshProducerAndRequiresRefresh(t *testing.T) {
	for _, requested := range []bool{false, true} {
		t.Run(fmt.Sprintf("requested=%t", requested), func(t *testing.T) {
			cfg, s := retryTesting(t, 1)
			failed := retryObservation(cfg, s, 2, "failure")
			failed.Runs = append(failed.Runs, CheckRun{ID: 2, Attempt: 1, Name: "lint", Source: "app:2", State: "failure"})
			s = foldCheckEvents(t, s, failed)
			old := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
			if requested {
				s = requestRetry(t, cfg, s, epoch)
				s = dispatchRetry(t, cfg, s, epoch, false)
			}
			mixed := failed
			mixed.Runs = slices.Clone(failed.Runs)
			mixed.Runs[0].ID = 3
			mixed.Runs[1].ID = 1
			s = foldCheckEvents(t, s, mixed)
			if s.Active.CheckRetry.ObservedRuns[0].ID != 3 || s.Active.CheckRetry.ObservedRuns[1].ID != 2 {
				t.Fatal("mixed observation lost a producer's newest identity")
			}
			onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
			// Neither a delayed coherent old result nor its passing variant can
			// authorize retry, failure confirmation or landing after the mixed read.
			s = foldCheckEvents(t, s, failed)
			passed := failed
			passed.Success = true
			passed.Runs = slices.Clone(failed.Runs)
			for i := range passed.Runs {
				passed.Runs[i].State = "success"
			}
			s = foldCheckEvents(t, s, passed)
			onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
			if requested {
				// The consumed budget stays bound to the original runs. A coherent
				// newer attempt for every failed producer confirms the second failure.
				partial := failed
				partial.Runs = slices.Clone(failed.Runs)
				partial.Runs[0].ID = 4
				s = foldCheckEvents(t, s, partial)
				if s.Active.CheckRetry.ObservedRuns[0].ID != 4 {
					t.Fatal("partially observed rerun lost its newest producer")
				}
				mixed.Runs[1].ID = 3
				s = foldCheckEvents(t, s, mixed)
				onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
				mixed.Runs[0].ID = 4
				s = foldCheckEvents(t, s, mixed)
				onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, epoch))
				if s.Active.CheckRetry.FailedRuns[0].ID != 2 {
					t.Fatal("mixed read replaced spent retry subject")
				}
			} else {
				mixed.Runs[1].ID = 2
				s = foldCheckEvents(t, s, mixed)
				next := onlyAction[RecordCheckRerun](t, Plan(cfg, s, epoch))
				if next.Runs[0].ID != 3 || next.Runs[1].ID != 2 || next.Key() == old.Key() {
					t.Fatal("coherent refresh reused stale retry subject or key")
				}
			}
		})
	}
}

func TestRetryOldestBatchSelection(t *testing.T) {
	oldest := &Batch{ID: "b1"}
	later := &Batch{ID: "b2"}
	if !retryOldestBatch(oldest, oldest) || retryOldestBatch(later, oldest) || retryOldestBatch(nil, oldest) || retryOldestBatch(later, nil) {
		t.Fatal("only the oldest provisional batch may consume a retry")
	}
}

func TestPermissionLostDuringRerunConfirmsUnknownOutcome(t *testing.T) {
	for _, outcome := range []string{"success", "pending", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			cfg, s := retryTesting(t, 1)
			s = foldCheckEvents(t, s, retryObservation(cfg, s, 701, "failure"))
			s = requestRetry(t, cfg, s, epoch)
			s = dispatchRetry(t, cfg, s, epoch, false)
			pending := retryObservation(cfg, s, 702, "pending")
			pending.Runs[0].Attempt = 2
			s = foldCheckEvents(t, s, pending)
			unknown := CheckRerunDispatched{BatchID: s.Active.ID, TestedSHA: "tested", Generation: s.Active.CheckRetry.Generation, OutcomeUnknown: true}
			stale := unknown
			stale.Generation++
			before := cloneState(s)
			s = foldCheckEvents(t, s, stale)
			if !reflect.DeepEqual(before.Active.CheckRetry, s.Active.CheckRetry) {
				t.Fatal("stale permission result changed retry")
			}
			// Permission loss must win even if a previously refreshed pass exists.
			if outcome == "success" {
				pass := retryObservation(cfg, s, 702, outcome)
				pass.Runs[0].Attempt = 2
				s = foldCheckEvents(t, s, pass)
			}
			s = foldCheckEvents(t, s, unknown)
			observed := retryObservation(cfg, s, 702, outcome)
			observed.Runs[0].Attempt = 2
			s = foldCheckEvents(t, s, observed, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: "tested", At: epoch.Add(time.Hour)})
			confirm := onlyAction[ConfirmCheckFailure](t, Plan(cfg, s, epoch.Add(2*time.Hour)))
			if confirm.Reason != ChecksFailed || s.ChecksPassed("tested") || s.Active.CheckRetry.Unsupported {
				t.Fatalf("permission loss became ordinary CI evidence: %+v", s.Active)
			}
			s = foldCheckEvents(t, s, ChecksFailureConfirmed(confirm))
			if detail := s.DequeueDetail(1, ChecksFailed); !strings.Contains(detail, ActionsPermissionLostDetail) || strings.Contains(detail, "timeout") {
				t.Fatal(detail)
			}
			if !s.Active.CheckRetry.Confirmed || s.Active.CheckRetry.RerunState != "unknown" {
				t.Fatalf("unknown outcome lost: %+v", s.Active.CheckRetry)
			}
		})
	}
}
