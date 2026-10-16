package queue

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewRound1Sequences(t *testing.T) {
	t.Run("early-success-and-failure", func(t *testing.T) {
		for _, success := range []bool{true, false} {
			s := queued(1)
			s = Apply(s, ChecksCompleted{TestedSHA: "early", Success: success})
			s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", At: epoch, Method: Merge})
			a := Plan(testConfig, s, epoch.Add(time.Hour))
			if success {
				onlyAction[Land](t, a)
			} else if d := onlyAction[Dequeue](t, a); d.Reason != ChecksFailed {
				t.Fatal(d)
			}
		}
	})
	t.Run("duplicate-dequeued-after-readmission", func(t *testing.T) {
		s := Apply(queued(1), DequeueRequested{PR: 1})
		d := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
		e := Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason, BatchID: d.BatchID}
		s = Apply(s, e)
		s = Apply(s, EnqueueRequested{PR: 1, At: epoch})
		before := Plan(testConfig, s, epoch)
		s = Apply(s, e)
		if len(s.Entries) != 1 || s.Entries[0].AdmissionID == d.AdmissionID || !reflect.DeepEqual(before, Plan(testConfig, s, epoch)) {
			t.Fatal("new admission changed")
		}
	})
	t.Run("transient-build-failure-bounded", func(t *testing.T) {
		cfg := testConfig
		cfg.BuildBackoff = time.Second
		s := queued(1)
		now := epoch
		for i := 0; i < 3; i++ {
			b := onlyAction[BuildTestRef](t, Plan(cfg, s, now))
			s = Apply(s, TestRefBuildFailed{BatchID: b.BatchID, At: now})
			if len(actionsOf[BuildTestRef](Plan(cfg, s, now))) != 0 {
				t.Fatal("immediate retry")
			}
			now = now.Add(time.Minute)
		}
		if s.PauseReason(cfg) != "build-failed" || len(s.Entries) != 1 || len(actionsOf[BuildTestRef](Plan(cfg, s, now.Add(time.Hour)))) != 0 {
			t.Fatal("not paused")
		}
	})
}

// The bounded cache may evict this outcome; a forge refresh must recover it.
func TestReviewNeededEarlyCheckSurvivesUnrelatedTraffic(t *testing.T) {
	for _, timeout := range []time.Duration{0, testConfig.CheckTimeout} {
		for _, success := range []bool{true, false} {
			t.Run(fmt.Sprintf("timeout=%s/success=%t", timeout, success), func(t *testing.T) {
				cfg := testConfig
				cfg.CheckTimeout = timeout
				s := queued(1)
				s = Apply(s, ChecksCompleted{TestedSHA: "needed", Success: success})
				for i := 0; i < 64; i++ {
					s = Apply(s, ChecksCompleted{TestedSHA: fmt.Sprintf("unrelated-%d", i), Success: true})
				}
				s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "needed", Method: Merge, At: epoch})
				now := epoch.Add(time.Hour)
				a := Plan(cfg, s, now)
				refresh := onlyAction[RefreshChecks](t, a)
				if refresh.BatchID != s.Active.ID || refresh.TestedSHA != "needed" || len(actionsOf[Dequeue](a)) != 0 {
					t.Fatalf("lost check must be reconciled before timeout: %v", descriptions(a))
				}
				// Executor re-reads the forge and logs the terminal outcome first.
				s = Apply(s, ChecksCompleted{TestedSHA: refresh.TestedSHA, Success: success})
				s = Apply(s, ChecksRefreshed{BatchID: refresh.BatchID, TestedSHA: refresh.TestedSHA, At: now})
				a = Plan(cfg, s, now)
				if success {
					onlyAction[Land](t, a)
				} else if d := onlyAction[Dequeue](t, a); d.Reason != ChecksFailed {
					t.Fatalf("needed failure became %s", d.Reason)
				}
			})
		}
	}
}

func TestReviewSameBaseObservationPreservesFailureBudget(t *testing.T) {
	cfg := testConfig
	cfg.BuildBackoff = time.Minute
	cfg.BuildRetries = 1
	s := queued(1)
	s = Apply(s, BaseMoved{SHA: "base1"})
	b := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
	s = Apply(s, TestRefBuildFailed{BatchID: b.BatchID, At: epoch})
	if s.PauseReason(cfg) != "build-failed" {
		t.Fatal("not paused")
	}
	s = Apply(s, BaseMoved{SHA: "base1"})
	if s.PauseReason(cfg) == "" || len(actionsOf[BuildTestRef](Plan(cfg, s, epoch))) != 0 {
		t.Fatalf("duplicate base observation resumed queue: %v", descriptions(Plan(cfg, s, epoch)))
	}
}

func TestReviewDelayedOldCheckOnReusedSHA(t *testing.T) {
	s := queued(1)
	s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "same", Method: Merge, At: epoch})
	s = Apply(s, ChecksCompleted{TestedSHA: "same", Success: false})
	d := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
	s = Apply(s, Dequeued{PR: 1, AdmissionID: d.AdmissionID, Reason: d.Reason})
	s = Apply(s, EnqueueRequested{PR: 1, At: epoch.Add(time.Minute)})
	b := onlyAction[BuildTestRef](t, Plan(testConfig, s, epoch))
	s = Apply(s, ChecksCompleted{TestedSHA: "same", Success: false}) // late old run result, during new build
	s = Apply(s, TestRefBuilt{BatchID: b.BatchID, TestedSHA: "same", Method: Merge, At: epoch.Add(time.Minute)})
	s = Apply(s, ChecksCompleted{TestedSHA: "same", Success: true}) // new run succeeds before any dequeue execution
	a := Plan(testConfig, s, epoch.Add(time.Minute))
	if len(actionsOf[Land](a)) != 0 || len(actionsOf[Dequeue](a)) != 0 || s.Active.Phase != Building || s.Active.ID == b.BatchID || s.buildRetry.failures != 1 {
		t.Fatalf("reused SHA was not rejected as a build failure: %v", descriptions(a))
	}
	var failureReports []ReportStatus
	for _, report := range actionsOf[ReportStatus](a) {
		if report.Status == "build-failed" {
			failureReports = append(failureReports, report)
		}
	}
	if len(failureReports) != 1 || !strings.Contains(failureReports[0].Detail, "already recorded for batch b1") {
		t.Fatalf("SHA reuse has no clear reason: %v", failureReports)
	}
	// A compliant retry uses a new test commit, so the old SHA cannot affect it.
	retry := onlyAction[BuildTestRef](t, a)
	s = Apply(s, TestRefBuilt{BatchID: retry.BatchID, TestedSHA: "unique", Method: Merge, At: epoch.Add(time.Minute)})
	s = Apply(s, ChecksCompleted{TestedSHA: "same", Success: false})
	s = Apply(s, ChecksCompleted{TestedSHA: "unique", Success: true})
	if land := onlyAction[Land](t, Plan(testConfig, s, epoch.Add(time.Minute))); land.TestedSHA != "unique" {
		t.Fatalf("old failure contaminated the unique retry: %+v", land)
	}
}

func TestReviewAdmissionDuringLanding(t *testing.T) {
	for _, change := range []Event{DequeueRequested{PR: 1}, PRObserved{PR: 1, HeadSHA: "head-new", Eligible: true, Trusted: true}} {
		for _, outcome := range []string{"landed", "rejected", "abandoned"} {
			t.Run(fmt.Sprintf("%T/%s", change, outcome), func(t *testing.T) {
				s := landingState()
				admission := s.Entries[0].AdmissionID
				s = Apply(s, change)
				if len(actionsOf[Dequeue](Plan(testConfig, s, epoch))) != 0 {
					t.Fatal("dequeue during landing")
				}
				s = Apply(s, Dequeued{PR: 1, AdmissionID: admission, Reason: Requested})
				if len(s.Entries) != 1 {
					t.Fatal("result removed landing admission")
				}
				var oldDequeue Dequeued
				switch outcome {
				case "landed":
					s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
				case "rejected":
					s = Apply(s, LandingRejected{BatchID: "b1", ActualBaseSHA: "base1", ActualFence: "f1"})
				case "abandoned":
					s = Apply(s, LandingAbandoned{BatchID: "b1", NewFence: "f1"})
				}
				if outcome != "landed" {
					d := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
					if d.AdmissionID != admission {
						t.Fatal("deferred dequeue lost admission")
					}
					oldDequeue = Dequeued{PR: 1, AdmissionID: admission, Reason: d.Reason, BatchID: d.BatchID}
					s = Apply(s, oldDequeue)
				} else {
					oldDequeue = Dequeued{PR: 1, AdmissionID: admission, Reason: Requested}
				}
				s = Apply(s, EnqueueRequested{PR: 1, At: epoch})
				before := Plan(testConfig, s, epoch)
				s = Apply(s, oldDequeue)
				s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
				if len(s.Entries) != 1 || s.Entries[0].AdmissionID == admission || !reflect.DeepEqual(before, Plan(testConfig, s, epoch)) {
					t.Fatal("stale result changed later admission")
				}
			})
		}
	}
}

func TestReviewPauseChangesAndBackoff(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 1
	s := queued(1, 2)
	s = Apply(s, TestRefBuildFailed{BatchID: s.Active.ID, At: epoch})
	r := onlyAction[ReportStatus](t, Plan(cfg, s, epoch))
	s = Apply(s, ActionCompleted{Key: r.Key()})
	s = Apply(s, PRObserved{PR: 1, HeadSHA: "changed", Eligible: true, Trusted: true})
	d := onlyAction[Dequeue](t, Plan(cfg, s, epoch))
	if d.Reason != HeadChanged {
		t.Fatal(d)
	}
	s = Apply(s, Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason})
	var pausedReports []ReportStatus
	for _, r := range actionsOf[ReportStatus](Plan(cfg, s, epoch)) {
		if r.Status == "build-failed" {
			pausedReports = append(pausedReports, r)
		}
	}
	if len(pausedReports) != 1 {
		t.Fatalf("pause reports=%v", pausedReports)
	}
	r2 := pausedReports[0]
	if r2.PR != 2 || r2.Key() == r.Key() || len(actionsOf[BuildTestRef](Plan(cfg, s, epoch))) != 0 {
		t.Fatal("pause lost on head dequeue")
	}
	s = Apply(s, ResumeRequested{})
	onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
	const maxDelay = time.Duration(1<<63 - 1)
	for _, delay := range []time.Duration{1, 2, time.Second, 1 << 62, maxDelay} {
		cfg.BuildBackoff = delay
		var prev time.Duration
		for count := 1; count <= 100; count++ {
			got := cfg.buildRetryDelay(count)
			if got < prev || got <= 0 {
				t.Fatalf("overflow: %d at %d -> %d", delay, count, got)
			}
			prev = got
		}
		if prev != maxDelay {
			t.Fatal("not saturated")
		}
	}
}

func TestReviewReplayDeterminism(t *testing.T) {
	events := []Event{PRObserved{PR: 1, HeadSHA: "head1", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}}
	for i := 0; i < 80; i++ {
		events = append(events, ChecksCompleted{TestedSHA: fmt.Sprintf("sha-%d", i), Success: i%2 == 0})
	}
	events = append(events, TestRefBuilt{BatchID: "b1", TestedSHA: "sha-79", Method: Squash, At: epoch})
	var reference State
	for replay := 0; replay < 100; replay++ {
		s := queued()
		for _, e := range events {
			s = Apply(s, e)
			before := cloneState(s)
			a := Plan(testConfig, s, epoch.Add(time.Hour))
			if !reflect.DeepEqual(a, Plan(testConfig, s, epoch.Add(time.Hour))) || !reflect.DeepEqual(s, before) {
				t.Fatal("Plan unstable or mutates state")
			}
			keys := map[string]bool{}
			for _, action := range a {
				if keys[action.Key()] {
					t.Fatalf("duplicate key %s", action.Key())
				}
				keys[action.Key()] = true
			}
		}
		if replay == 0 {
			reference = s
		} else if !reflect.DeepEqual(reference, s) {
			t.Fatal("Apply replay nondeterministic")
		}
	}
}

func TestReviewReusedSHAOrdering(t *testing.T) {
	s := queued(1)
	s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "same", Method: Merge, At: epoch})
	s = Apply(s, ChecksCompleted{TestedSHA: "same", Success: false})
	d := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
	s = Apply(s, Dequeued{PR: 1, AdmissionID: d.AdmissionID, Reason: d.Reason})
	s = Apply(s, EnqueueRequested{PR: 1, At: epoch})
	built := TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "same", Method: Merge, At: epoch}
	oldFailure := ChecksCompleted{TestedSHA: "same", Success: false}
	freshSuccess := ChecksCompleted{TestedSHA: "same", Success: true}
	for _, tc := range []struct {
		name   string
		events []Event
	}{
		{"both-before-built", []Event{oldFailure, freshSuccess, built}},
		{"built-between-outcomes", []Event{oldFailure, built, freshSuccess}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := s
			for _, e := range tc.events {
				next = Apply(next, e)
			}
			a := Plan(testConfig, next, epoch)
			if len(actionsOf[Land](a)) != 0 || len(actionsOf[Dequeue](a)) != 0 || next.Active.Phase != Building || next.buildRetry.failures != 1 {
				t.Fatalf("reused SHA was accepted under reordered outcomes: %v", descriptions(a))
			}
			onlyAction[BuildTestRef](t, a)
		})
	}
}

func TestReviewFailureBudgetUnderRepeatedBaseObservation(t *testing.T) {
	cfg := testConfig
	cfg.BuildBackoff = time.Hour
	cfg.BuildRetries = 3
	s := queued(1)
	now := epoch
	for i := 0; i < 3; i++ {
		b := onlyAction[BuildTestRef](t, Plan(cfg, s, now))
		s = Apply(s, TestRefBuildFailed{BatchID: b.BatchID, At: now})
		before := s.buildRetry
		for observation := 0; observation < 10; observation++ {
			s = Apply(s, BaseMoved{SHA: "base1"})
			if s.buildRetry != before || len(actionsOf[BuildTestRef](Plan(cfg, s, now))) != 0 {
				t.Fatal("unchanged base reset the failure streak or bypassed backoff")
			}
		}
		now = now.Add(cfg.buildRetryDelay(i + 1))
	}
	if len(actionsOf[BuildTestRef](Plan(cfg, s, now.Add(24*time.Hour)))) != 0 || s.PauseReason(cfg) != "build-failed" {
		t.Fatal("unchanged base resumed the paused queue")
	}
}

func TestReviewMethodsAndKeys(t *testing.T) {
	for _, method := range []MergeMethod{Merge, Squash, Rebase} {
		cfg := testConfig
		cfg.MergeMethod = method
		s := queued(1)
		b := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
		again := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(time.Hour)))
		if b.Key() != again.Key() || b.Method != method {
			t.Fatal("unstable build key")
		}
		s = Apply(s, TestRefBuilt{BatchID: b.BatchID, TestedSHA: "sha", Method: b.Method, At: epoch})
		s = Apply(s, ChecksCompleted{TestedSHA: "sha", Success: true})
		cfg.MergeMethod = Merge
		l := onlyAction[Land](t, Plan(cfg, s, epoch))
		if l.Method != method {
			t.Fatal("method not pinned")
		}
		if l.Key() != onlyAction[Land](t, Plan(cfg, s, epoch.Add(time.Hour))).Key() {
			t.Fatal("unstable land key")
		}
	}
}

func TestChecksRefreshCadenceAndKeys(t *testing.T) {
	for _, interval := range []time.Duration{0, 2 * time.Minute} {
		t.Run(fmt.Sprintf("interval=%s", interval), func(t *testing.T) {
			cfg := testConfig
			cfg.CheckTimeout = 0
			cfg.RefreshInterval = interval
			if interval == 0 {
				interval = time.Minute
			}
			s := testingState()
			first := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
			if first.BatchID != "b1" || first.TestedSHA != "tested1" {
				t.Fatalf("refresh omitted its subject: %+v", first)
			}
			if again := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch.Add(time.Hour))); again.Key() != first.Key() {
				t.Fatal("time changed the key before the refresh was recorded")
			}
			s = Apply(s, ActionCompleted{Key: first.Key()})
			if again := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch)); again.Key() != first.Key() {
				t.Fatal("effect acknowledgement completed a checks refresh")
			}
			s = Apply(s, ChecksRefreshed{BatchID: first.BatchID, TestedSHA: first.TestedSHA, At: epoch})
			assertPlan(t, cfg, s, epoch)
			assertPlan(t, cfg, s, epoch.Add(interval-time.Nanosecond))
			second := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch.Add(interval)))
			if second.Key() == first.Key() {
				t.Fatal("recorded refresh did not advance the epoch")
			}
			s = Apply(s, ChecksCompleted{TestedSHA: "unrelated", Success: true})
			if again := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch.Add(2*interval))); again.Key() != second.Key() {
				t.Fatal("unrelated observation or replanning changed the refresh key")
			}
			s = Apply(s, ChecksRefreshed{BatchID: second.BatchID, TestedSHA: second.TestedSHA, At: epoch.Add(interval)})
			assertPlan(t, cfg, s, epoch.Add(2*interval-time.Nanosecond))
			third := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch.Add(2*interval)))
			if third.Key() == second.Key() {
				t.Fatal("second recorded refresh did not advance the epoch")
			}
			s = Apply(s, BaseMoved{SHA: "base2"})
			s = Apply(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "tested2", Method: Merge, At: epoch})
			next := onlyAction[RefreshChecks](t, Plan(cfg, s, epoch))
			if next.Key() == first.Key() || next.BatchID != s.Active.ID || next.TestedSHA != "tested2" {
				t.Fatal("new batch reused the prior refresh action")
			}
		})
	}
}

func TestChecksTimeoutRequiresDeadlineRefresh(t *testing.T) {
	cfg := testConfig
	cfg.RefreshInterval = time.Hour
	deadline := epoch.Add(cfg.CheckTimeout)
	now := deadline.Add(time.Nanosecond)
	for _, refreshAt := range []time.Time{deadline.Add(-time.Nanosecond), deadline, now} {
		t.Run(refreshAt.Format(time.RFC3339Nano), func(t *testing.T) {
			s := testingState()
			s = Apply(s, ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: refreshAt})
			a := Plan(cfg, s, now)
			if refreshAt.Before(deadline) {
				onlyAction[RefreshChecks](t, a)
				if len(actionsOf[Dequeue](a)) != 0 || len(actionsOf[DeleteRef](a)) != 0 {
					t.Fatalf("pre-deadline refresh allowed a timeout: %v", descriptions(a))
				}
				return
			}
			d := onlyAction[Dequeue](t, a)
			if d.Reason != ChecksTimeout || d.BatchID != s.Active.ID || d.AdmissionID != s.Entries[0].AdmissionID || len(actionsOf[RefreshChecks](a)) != 0 {
				t.Fatalf("post-deadline refresh did not permit correlated timeout: %v", descriptions(a))
			}
			onlyAction[DeleteRef](t, a)
		})
	}
}

func TestChecksRefreshRejectsStaleSubjects(t *testing.T) {
	now := epoch.Add(time.Hour)
	for _, event := range []ChecksRefreshed{
		{BatchID: "old", TestedSHA: "tested1", At: now},
		{BatchID: "b1", TestedSHA: "old", At: now},
	} {
		t.Run(fmt.Sprintf("%s/%s", event.BatchID, event.TestedSHA), func(t *testing.T) {
			s := testingState()
			before := onlyAction[RefreshChecks](t, Plan(testConfig, s, now))
			s = Apply(s, event)
			a := Plan(testConfig, s, now)
			if after := onlyAction[RefreshChecks](t, a); after.Key() != before.Key() || len(actionsOf[Dequeue](a)) != 0 {
				t.Fatal("stale refresh advanced the epoch or enabled a timeout")
			}
		})
	}
	for _, initial := range []func() State{func() State { return queued(1) }, landingState} {
		s := initial()
		before := Plan(testConfig, s, now)
		s = Apply(s, ChecksRefreshed{BatchID: "b1", TestedSHA: s.Active.TestedSHA, At: now})
		if s.Active.checkRefreshes != 0 || !reflect.DeepEqual(before, Plan(testConfig, s, now)) {
			t.Fatal("refresh changed a batch outside Testing")
		}
	}
}

func TestChecksRefreshTimeDoesNotRegress(t *testing.T) {
	s := testingState()
	now := epoch.Add(time.Minute)
	s = Apply(s, ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: now})
	s = Apply(s, ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: epoch})
	assertPlan(t, testConfig, s, now)
	onlyAction[RefreshChecks](t, Plan(testConfig, s, now.Add(time.Minute)))
}

func TestTestedSHAOwnershipSurvivesBatchCompletion(t *testing.T) {
	for _, transition := range []string{"base-moved", "dequeued", "landed", "rejected", "abandoned"} {
		t.Run(transition, func(t *testing.T) {
			cfg := testConfig
			cfg.BuildBackoff = time.Minute
			cfg.BuildRetries = 1
			s := queued(1, 2)
			s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested1", Method: Merge, At: epoch})
			// A retried result for this same batch must not count as SHA reuse.
			s = Apply(s, TestRefBuilt{BatchID: "b1", TestedSHA: "tested1", Method: Merge, At: epoch})
			if s.buildRetry.failures != 0 || s.Active.Phase != Testing {
				t.Fatal("duplicate build result failed its own batch")
			}
			switch transition {
			case "base-moved":
				s = Apply(s, BaseMoved{SHA: "base2"})
			case "dequeued":
				s = Apply(s, DequeueRequested{PR: 1})
				d := onlyAction[Dequeue](t, Plan(cfg, s, epoch))
				s = Apply(s, Dequeued{PR: d.PR, AdmissionID: d.AdmissionID, Reason: d.Reason})
			default:
				s = Apply(s, ChecksCompleted{TestedSHA: "tested1", Success: true})
				s = Apply(s, LandingStarted{BatchID: "b1", AttemptID: "attempt1", FenceToken: "f0", At: epoch})
				switch transition {
				case "landed":
					s = Apply(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
				case "rejected":
					s = Apply(s, LandingRejected{BatchID: "b1", ActualBaseSHA: "base1", ActualFence: "f1"})
				case "abandoned":
					s = Apply(s, LandingAbandoned{BatchID: "b1", NewFence: "f1"})
				}
			}
			s = Apply(s, ChecksCompleted{TestedSHA: "tested1", Success: false})
			if len(s.checks) != 0 {
				t.Fatal("late outcome for discarded batch was retained")
			}
			batchID := s.Active.ID
			s = Apply(s, TestRefBuilt{BatchID: batchID, TestedSHA: "tested1", Method: Merge, At: epoch})
			a := Plan(cfg, s, epoch)
			if s.Active.ID == batchID || s.Active.Phase != Building || s.PauseReason(cfg) != "build-failed" || len(actionsOf[BuildTestRef](a)) != 0 || len(actionsOf[Dequeue](a)) != 0 {
				t.Fatalf("SHA reuse was not a non-conflict build failure: %v", descriptions(a))
			}
			var report ReportStatus
			for _, r := range actionsOf[ReportStatus](a) {
				if r.Status == "build-failed" {
					report = r
				}
			}
			if !strings.Contains(report.Detail, "tested SHA tested1 was already recorded for batch b1") {
				t.Fatalf("missing reuse explanation: %+v", report)
			}
			s = Apply(s, ActionCompleted{Key: report.Key()})
			for _, r := range actionsOf[ReportStatus](Plan(cfg, s, epoch)) {
				if r.Key() == report.Key() {
					t.Fatal("acknowledged reuse failure report repeated")
				}
			}
		})
	}
}
