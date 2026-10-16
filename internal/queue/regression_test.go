package queue

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func actionsOf[T Action](actions []Action) []T {
	var found []T
	for _, action := range actions {
		if typed, ok := action.(T); ok {
			found = append(found, typed)
		}
	}
	return found
}

func onlyAction[T Action](t *testing.T, actions []Action) T {
	t.Helper()
	found := actionsOf[T](actions)
	if len(found) != 1 {
		t.Fatalf("expected one %T, got %v", *new(T), actions)
	}
	return found[0]
}

func TestChecksBeforeTestRefBuilt(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "failure"
		if success {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			s := queued(1)
			batchID := s.Active.ID
			s = applySerial(s, ChecksCompleted{TestedSHA: "early", Success: success, At: epoch})
			s = applySerial(s, TestRefBuilt{Method: Merge, BatchID: batchID, TestedSHA: "early", At: epoch})
			actions := Plan(testConfig, s, epoch.Add(time.Hour))
			if success {
				land := onlyAction[Land](t, actions)
				if land.TestedSHA != "early" || land.ExpectedBaseSHA != "base1" {
					t.Fatalf("early success lost tested commit: %+v", land)
				}
			} else if dequeue := onlyAction[Dequeue](t, actions); dequeue.Reason != ChecksFailed {
				t.Fatalf("early failure became %q, want checks-failed", dequeue.Reason)
			}
		})
	}
}

func TestRetainedChecksArePruned(t *testing.T) {
	for _, transition := range []string{"base-moved", "build-failed", "dequeued", "landed", "rejected", "abandoned"} {
		t.Run(transition, func(t *testing.T) {
			s := queued(1, 2)
			batchID := s.Active.ID
			s = applySerial(s, ChecksCompleted{TestedSHA: "retained", Success: true, At: epoch})
			switch transition {
			case "base-moved":
				s = applySerial(s, BaseMoved{SHA: "base2"})
			case "build-failed":
				s = applySerial(s, TestRefBuildFailed{BatchID: batchID, At: epoch})
			case "dequeued":
				s = applySerial(s, DequeueRequested{PR: 1})
				dequeue := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
				s = applySerial(s, Dequeued{PR: dequeue.PR, AdmissionID: dequeue.AdmissionID, Reason: dequeue.Reason})
			default:
				s = applySerial(s, TestRefBuilt{BatchID: batchID, TestedSHA: "first", Method: Merge, At: epoch})
				s = applySerial(s, ChecksCompleted{TestedSHA: "first", Success: true, At: epoch})
				s = applySerial(s, LandingStarted{BatchID: batchID, AttemptID: "attempt", FenceToken: "f0", At: epoch})
				switch transition {
				case "landed":
					s = applySerial(s, Landed{BatchID: batchID, NewBaseSHA: "first", NewFence: "f1"})
				case "rejected":
					s = applySerial(applySerial(s, BaseMoved{SHA: "base2"}), LandingAbandoned{BatchID: batchID, NewFence: "f1"})
				case "abandoned":
					s = applySerial(s, LandingAbandoned{BatchID: batchID, NewFence: "f1"})
				}
			}
			build := onlyAction[BuildTestRef](t, Plan(testConfig, s, epoch))
			if build.BatchID == batchID {
				t.Fatal("discarded batch reused")
			}
			s = applySerial(s, TestRefBuilt{BatchID: build.BatchID, TestedSHA: "retained", Method: build.Method, At: epoch})
			if actions := Plan(testConfig, s, epoch); len(actionsOf[Land](actions)) != 0 || len(actionsOf[Dequeue](actions)) != 0 {
				t.Fatalf("old outcome crossed batch lifetime: %v", actions)
			}
			s = applySerial(s, ChecksCompleted{TestedSHA: "retained", Success: true, At: epoch})
			onlyAction[Land](t, Plan(testConfig, s, epoch))
		})
	}
}

func TestRetainedChecksCapacity(t *testing.T) {
	s := queued(1)
	for i := 0; i <= maxRetainedChecks; i++ {
		s = applySerial(s, ChecksCompleted{TestedSHA: fmt.Sprintf("sha-%d", i), Success: true, At: epoch})
	}
	for _, tc := range []struct {
		name string
		sha  string
		land bool
	}{
		{"oldest evicted", "sha-0", false},
		{"next oldest retained", "sha-1", true},
		{"newest retained", fmt.Sprintf("sha-%d", maxRetainedChecks), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: tc.sha, Method: Merge, At: epoch})
			lands := actionsOf[Land](Plan(testConfig, next, epoch))
			if (len(lands) == 1) != tc.land {
				t.Fatalf("landing for %s = %v, want enabled=%t", tc.sha, lands, tc.land)
			}
		})
	}
}

func TestRetainedChecksUseLatestOutcome(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("latest success=%t", success), func(t *testing.T) {
			s := queued(1)
			s = applySerial(s, ChecksCompleted{TestedSHA: "early", Success: !success, At: epoch})
			s = applySerial(s, ChecksCompleted{TestedSHA: "early", Success: success, At: epoch})
			s = applySerial(s, TestRefBuilt{BatchID: s.Active.ID, TestedSHA: "early", Method: Merge, At: epoch})
			actions := Plan(testConfig, s, epoch)
			if success {
				onlyAction[Land](t, actions)
			} else if dequeue := onlyAction[Dequeue](t, actions); dequeue.Reason != ChecksFailed {
				t.Fatalf("latest failed outcome lost: %+v", dequeue)
			}
		})
	}
}

func TestStaleDequeueCannotRemoveNewAdmission(t *testing.T) {
	s := applySerial(queued(1), DequeueRequested{PR: 1})
	first := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
	result := Dequeued{PR: first.PR, AdmissionID: first.AdmissionID, Reason: first.Reason}
	s = applySerial(s, result)
	s = applySerial(s, EnqueueRequested{PR: 1, At: epoch.Add(time.Minute)})
	before := Plan(testConfig, s, epoch)
	if s.Entries[0].AdmissionID <= first.AdmissionID {
		t.Fatal("new admission did not advance its ID")
	}
	s = applySerial(s, result)
	if len(s.Entries) != 1 || !reflect.DeepEqual(before, Plan(testConfig, s, epoch)) {
		t.Fatal("delayed duplicate dequeued the new admission")
	}
	s = applySerial(s, DequeueRequested{PR: 1})
	second := onlyAction[Dequeue](t, Plan(testConfig, s, epoch))
	if first.Key() == second.Key() || second.AdmissionID != s.Entries[0].AdmissionID {
		t.Fatal("dequeue action did not identify the new admission")
	}
	changedAdmission := first
	changedAdmission.AdmissionID = second.AdmissionID
	if changedAdmission.Key() == first.Key() {
		t.Fatal("dequeue key omits admission ID")
	}
	s = applySerial(s, result)
	if got := onlyAction[Dequeue](t, Plan(testConfig, s, epoch)); got.Key() != second.Key() {
		t.Fatal("old result completed a pending dequeue for the new admission")
	}
	s = applySerial(s, Dequeued{PR: second.PR, AdmissionID: second.AdmissionID, Reason: second.Reason})
	if len(s.Entries) != 0 || s.Active != nil {
		t.Fatal("matching dequeue result did not complete")
	}
}

func TestStaleTimeoutCannotRemoveRebuiltBatch(t *testing.T) {
	s := testingState()
	s = applySerial(s, ChecksRefreshed{BatchID: s.Active.ID, TestedSHA: s.Active.TestedSHA, At: epoch.Add(time.Hour)})
	timeout := onlyAction[Dequeue](t, Plan(testConfig, s, epoch.Add(time.Hour)))
	if timeout.BatchID != s.Active.ID || timeout.AdmissionID != s.Entries[0].AdmissionID {
		t.Fatalf("timeout omitted its subject: %+v", timeout)
	}
	s = applySerial(s, BaseMoved{SHA: "base2"})
	before := Plan(testConfig, s, epoch)
	s = applySerial(s, Dequeued{PR: timeout.PR, AdmissionID: timeout.AdmissionID, BatchID: timeout.BatchID, Reason: timeout.Reason})
	if len(s.Entries) != 1 || !reflect.DeepEqual(before, Plan(testConfig, s, epoch)) {
		t.Fatal("old timeout removed the rebuilt batch")
	}
}

func withBuildFailures(t *testing.T, cfg Config, count int) State {
	t.Helper()
	s := queued(1)
	for i := 0; i < count; i++ {
		build := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(time.Hour)))
		s = applySerial(s, TestRefBuildFailed{BatchID: build.BatchID, At: epoch})
	}
	return s
}

func TestBuildFailuresPause(t *testing.T) {
	for _, limit := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			cfg := testConfig
			cfg.BuildRetries = limit
			effectiveLimit := limit
			if effectiveLimit == 0 {
				effectiveLimit = 3
			}
			s := withBuildFailures(t, cfg, effectiveLimit-1)
			build := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
			s = applySerial(s, TestRefBuildFailed{BatchID: build.BatchID, At: epoch})
			actions := Plan(cfg, s, epoch.Add(24*time.Hour))
			if s.PauseReason(cfg) != "build-failed" || len(actionsOf[BuildTestRef](actions)) != 0 || len(actionsOf[Dequeue](actions)) != 0 || len(s.Entries) != 1 {
				t.Fatalf("failure limit did not pause the queued PR: %+v, %v", s, actions)
			}
			report := onlyAction[ReportStatus](t, actions)
			if report.PR != 1 || report.Reason != "build-failed" || !strings.Contains(report.Detail, "Queue paused") || !strings.Contains(report.Detail, fmt.Sprint(effectiveLimit)) {
				t.Fatalf("pause is not explained: %+v", report)
			}
			s = applySerial(s, PRObserved{PR: 1, HeadSHA: "head1", Eligible: true, Trusted: true})
			if again := onlyAction[ReportStatus](t, Plan(cfg, s, epoch)); again.Key() != report.Key() {
				t.Fatal("unchanged pause changed its report key")
			}
			s = applySerial(s, ActionCompleted{Key: report.Key()})
			actions = Plan(cfg, s, epoch.Add(48*time.Hour))
			if len(actionsOf[ReportStatus](actions)) != 0 || len(actionsOf[BuildTestRef](actions)) != 0 {
				t.Fatalf("acknowledged pause repeated or resumed: %v", actions)
			}
		})
	}
}

func TestBuildBackoff(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 5
	cfg.BuildBackoff = 5 * time.Second
	s := queued(1)
	for failure := 1; failure <= 3; failure++ {
		at := epoch.Add(time.Duration(failure) * time.Minute)
		build := onlyAction[BuildTestRef](t, Plan(cfg, s, at))
		s = applySerial(s, TestRefBuildFailed{BatchID: build.BatchID, At: at})
		delay := cfg.BuildBackoff * time.Duration(1<<(failure-1))
		if actions := Plan(cfg, s, at.Add(delay-time.Nanosecond)); len(actionsOf[BuildTestRef](actions)) != 0 || len(actionsOf[Dequeue](actions)) != 0 {
			t.Fatalf("failure %d retried before %s: %v", failure, delay, actions)
		}
		retry := onlyAction[BuildTestRef](t, Plan(cfg, s, at.Add(delay)))
		after := onlyAction[BuildTestRef](t, Plan(cfg, s, at.Add(delay+time.Nanosecond)))
		if retry.BatchID == build.BatchID || retry.Key() != after.Key() {
			t.Fatal("rebuild did not get a new stable key")
		}
	}
}

func TestBuildPauseResume(t *testing.T) {
	for _, event := range []Event{ResumeRequested{}, BaseMoved{SHA: "base2"}} {
		t.Run(fmt.Sprintf("%T", event), func(t *testing.T) {
			cfg := testConfig
			cfg.BuildRetries = 2
			cfg.BuildBackoff = time.Minute
			s := withBuildFailures(t, cfg, 2)
			onlyAction[ReportStatus](t, Plan(cfg, s, epoch))
			s = applySerial(s, event)
			build := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
			if s.PauseReason(cfg) != "" || len(actionsOf[ReportStatus](Plan(cfg, s, epoch))) != 0 {
				t.Fatal("resume retained the pause")
			}
			if moved, ok := event.(BaseMoved); ok && build.BaseSHA != moved.SHA {
				t.Fatal("base move resumed on the old base")
			}
			s = applySerial(s, TestRefBuildFailed{BatchID: build.BatchID, At: epoch})
			if s.PauseReason(cfg) != "" {
				t.Fatal("resume did not reset the failure count")
			}
			onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(time.Minute)))
		})
	}
}

func TestPauseReportAcknowledgementCannotCompleteNewAdmissionReport(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 1
	s := withBuildFailures(t, cfg, 1)
	first := onlyAction[ReportStatus](t, Plan(cfg, s, epoch))
	s = applySerial(s, DequeueRequested{PR: 1})
	dequeue := onlyAction[Dequeue](t, Plan(cfg, s, epoch))
	s = applySerial(s, Dequeued{PR: dequeue.PR, AdmissionID: dequeue.AdmissionID, Reason: dequeue.Reason})
	for _, effect := range Plan(cfg, s, epoch) {
		s = applySerial(s, ActionCompleted{Key: effect.Key()})
	}
	s = applySerial(s, EnqueueRequested{PR: 1, At: epoch})
	second := onlyAction[ReportStatus](t, Plan(cfg, s, epoch))
	if first.Key() == second.Key() {
		t.Fatal("new admission reused a pause report key")
	}
	s = applySerial(s, ActionCompleted{Key: first.Key()})
	if report := onlyAction[ReportStatus](t, Plan(cfg, s, epoch)); report.Key() != second.Key() {
		t.Fatal("stale acknowledgement completed the new admission's pause report")
	}
}

func TestBuildBackoffSaturates(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 4
	s := withBuildFailures(t, cfg, 2)
	cfg.BuildBackoff = 1 << 62
	if actions := Plan(cfg, s, epoch.Add(cfg.BuildBackoff)); len(actionsOf[BuildTestRef](actions)) != 0 {
		t.Fatal("overflow bypassed the doubled backoff")
	}
	onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(time.Duration(1<<63-1))))
}

func TestSuccessfulBuildResetsFailures(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 2
	cfg.BuildBackoff = 5 * time.Second
	s := withBuildFailures(t, cfg, 1)
	build := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(time.Minute)))
	s = applySerial(s, TestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested", Method: build.Method, At: epoch})
	s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true, At: epoch})
	s = applySerial(s, LandingStarted{BatchID: build.BatchID, AttemptID: "attempt", FenceToken: "f0", At: epoch})
	s = applySerial(applySerial(s, BaseMoved{SHA: "base1"}), LandingAbandoned{BatchID: build.BatchID, NewFence: "f1"})
	build = onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
	s = applySerial(s, TestRefBuildFailed{BatchID: build.BatchID, At: epoch})
	if s.PauseReason(cfg) != "" {
		t.Fatal("successful build did not reset the failure count")
	}
	onlyAction[BuildTestRef](t, Plan(cfg, s, epoch.Add(cfg.BuildBackoff)))
}

func TestStaleBuildFailureCannotPauseNewBatch(t *testing.T) {
	cfg := testConfig
	cfg.BuildRetries = 2
	s := queued(1)
	failure := TestRefBuildFailed{BatchID: s.Active.ID, At: epoch}
	s = applySerial(s, failure)
	before := Plan(cfg, s, epoch)
	s = applySerial(s, failure)
	if s.PauseReason(cfg) != "" || !reflect.DeepEqual(before, Plan(cfg, s, epoch)) {
		t.Fatal("stale build failure affected the new batch")
	}
}

func TestMergeMethodPinnedBetweenBuildAndLand(t *testing.T) {
	for _, actual := range []MergeMethod{Merge, Squash, Rebase} {
		t.Run(string(actual), func(t *testing.T) {
			s := queued(1)
			cfg := testConfig
			cfg.MergeMethod = actual
			build := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
			if build.Method != actual {
				t.Fatalf("build method = %q, want %q", build.Method, actual)
			}
			cfg.MergeMethod = Squash
			if actual == Squash {
				cfg.MergeMethod = Rebase
			}
			changed := onlyAction[BuildTestRef](t, Plan(cfg, s, epoch))
			if changed.Method != cfg.MergeMethod || changed.Key() == build.Key() {
				t.Fatal("build key did not distinguish the configured method")
			}
			s = applySerial(s, TestRefBuilt{BatchID: build.BatchID, TestedSHA: "tested", Method: build.Method, At: epoch})
			s = applySerial(s, ChecksCompleted{TestedSHA: "tested", Success: true, At: epoch})
			land := onlyAction[Land](t, Plan(cfg, s, epoch))
			if land.Method != actual || s.Active.Method != actual || land.TestedSHA != "tested" || land.ExpectedBaseSHA != build.BaseSHA || land.FenceToken != s.Fence {
				t.Fatalf("config change altered landing payload: %+v", land)
			}
		})
	}
}
