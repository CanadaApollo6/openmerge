package queue

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

var testConfig = Config{MergeMethod: Merge, CheckTimeout: 10 * time.Minute, ResolveAfter: time.Minute}

func queued(prs ...int) State {
	s := State{BaseBranch: "main", BaseSHA: "base1", Fence: "f0"}
	for _, pr := range prs {
		s = applySerial(s, PRObserved{PR: pr, HeadSHA: fmt.Sprintf("head%d", pr), Eligible: true, Trusted: true})
		s = applySerial(s, EnqueueRequested{PR: pr, At: epoch})
	}
	return s
}

func testingState() State {
	return applySerial(queued(1), TestRefBuilt{Method: Merge, BatchID: "b1", TestedSHA: "tested1", At: epoch})
}

func readyState() State {
	return applySerial(testingState(), ChecksCompleted{TestedSHA: "tested1", Success: true})
}

func landingState() State {
	return applySerial(readyState(), LandingStarted{BatchID: "b1", AttemptID: "attempt1", FenceToken: "f0", At: epoch})
}

func descriptions(actions []Action) []string {
	var result []string
	for _, action := range actions {
		switch a := action.(type) {
		case BuildTestRef:
			result = append(result, fmt.Sprintf("build:%s:%s:%v", a.BatchID, a.BaseSHA, a.PRs))
		case RefreshChecks:
			result = append(result, "refresh:"+a.BatchID+":"+a.TestedSHA)
		case Land:
			result = append(result, fmt.Sprintf("land:%s:%s:%s:%s", a.BatchID, a.ExpectedBaseSHA, a.TestedSHA, a.FenceToken))
		case ResolveLanding:
			result = append(result, "resolve:"+a.BatchID+":"+a.AttemptID)
		case DeleteRef:
			result = append(result, "delete:"+a.Ref)
		case Dequeue:
			result = append(result, fmt.Sprintf("dequeue:%d:%s", a.PR, a.Reason))
		case ReportStatus:
			result = append(result, fmt.Sprintf("report:%d:%s:%s", a.PR, a.Status, a.Detail))
		}
	}
	return result
}

func assertPlan(t *testing.T, cfg Config, s State, now time.Time, want ...string) []Action {
	t.Helper()
	got := Plan(cfg, s, now)
	if !slices.Equal(descriptions(got), want) {
		t.Fatalf("Plan = %v, want %v", descriptions(got), want)
	}
	if again := Plan(cfg, s, now); !reflect.DeepEqual(got, again) {
		t.Fatalf("repeated Plan changed actions: %#v != %#v", got, again)
	}
	keys := make(map[string]bool)
	for _, action := range got {
		if err := action.Validate(); err != nil {
			t.Fatalf("Plan emitted invalid %T: %v", action, err)
		}
		if action.Key() == "" || keys[action.Key()] {
			t.Fatalf("empty or duplicate action key: %q", action.Key())
		}
		keys[action.Key()] = true
		if build, ok := action.(BuildTestRef); ok {
			for _, pr := range build.PRs {
				observation := s.observations[pr.PR]
				if !observation.Trusted || observation.HeadSHA != pr.HeadSHA {
					t.Fatalf("untrusted head in BuildTestRef: %+v", pr)
				}
			}
		}
	}
	return got
}

func TestApplyAndPlan(t *testing.T) {
	tests := []struct {
		name       string
		initial    func() State
		events     []Event
		entries    []int
		batch      string
		phase      Phase
		base       string
		fence      string
		want       []string
		checkState func(*testing.T, State)
	}{
		{
			name: "trusted eligible enqueue", initial: func() State { return queued() },
			events:  []Event{PRObserved{PR: 1, HeadSHA: "head1", Eligible: true, Trusted: true}, EnqueueRequested{PR: 1, At: epoch}},
			entries: []int{1}, batch: "b1", phase: Building,
			want: []string{"build:b1:base1:[{1 head1}]"},
		},
		{
			name: "enqueue without observation", initial: func() State { return queued() },
			events: []Event{EnqueueRequested{PR: 1, At: epoch}},
			want:   []string{"report:1:blocked:PR does not satisfy queue entry conditions"},
		},
		{
			name: "ineligible enqueue", initial: func() State { return queued() },
			events: []Event{PRObserved{PR: 1, HeadSHA: "head1", Trusted: true}, EnqueueRequested{PR: 1, At: epoch}},
			want:   []string{"report:1:blocked:PR does not satisfy queue entry conditions"},
		},
		{
			name: "untrusted enqueue", initial: func() State { return queued() },
			events: []Event{PRObserved{PR: 1, HeadSHA: "head1", Eligible: true}, EnqueueRequested{PR: 1, At: epoch}},
			want:   []string{"report:1:blocked:PR head has not passed the trust gate"},
		},
		{
			name: "duplicate enqueue", initial: func() State { return queued(1) },
			events:  []Event{EnqueueRequested{PR: 1, At: epoch.Add(time.Hour)}},
			entries: []int{1}, batch: "b1", phase: Building,
			want: []string{"build:b1:base1:[{1 head1}]"},
			checkState: func(t *testing.T, s State) {
				if s.Entries[0].EnqueuedAt != epoch {
					t.Fatal("duplicate enqueue replaced original enqueue time")
				}
			},
		},
		{
			name: "FIFO follows enqueue order", initial: func() State { return queued(1, 2) },
			events:  []Event{PRObserved{PR: 3, HeadSHA: "head3", Eligible: true, Trusted: true}, EnqueueRequested{PR: 3, At: epoch.Add(-time.Hour)}},
			entries: []int{1, 2, 3}, batch: "b1", phase: Building,
			want: []string{"build:b1:base1:[{1 head1}]"},
		},
		{
			name: "built starts testing", initial: func() State { return queued(1) },
			events:  []Event{TestRefBuilt{Method: Merge, BatchID: "b1", TestedSHA: "tested1", At: epoch}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"refresh:b1:tested1"},
			checkState: func(t *testing.T, s State) {
				if s.Active.TestedSHA != "tested1" || s.Active.StartedTestingAt != epoch {
					t.Fatal("test SHA or event timestamp lost")
				}
			},
		},
		{
			name: "matching checks permit exact SHA landing", initial: testingState,
			events:  []Event{ChecksCompleted{TestedSHA: "tested1", Success: true}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"land:b1:base1:tested1:f0"},
		},
		{
			name: "unrelated successful checks ignored", initial: testingState,
			events:  []Event{ChecksCompleted{TestedSHA: "old-sha", Success: true}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"refresh:b1:tested1"},
		},
		{
			name: "unrelated failed checks ignored", initial: testingState,
			events:  []Event{ChecksCompleted{TestedSHA: "old-sha", Success: false}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"refresh:b1:tested1"},
		},
		{
			name: "failed checks request dequeue", initial: testingState,
			events:  []Event{ChecksCompleted{TestedSHA: "tested1", Success: false}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"dequeue:1:checks-failed", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "conflict requests dequeue", initial: func() State { return queued(1) },
			events:  []Event{TestRefBuildFailed{BatchID: "b1", Conflict: true}},
			entries: []int{1}, batch: "b1", phase: Building,
			want: []string{"dequeue:1:conflict", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "transient build failure retries new batch", initial: func() State { return queued(1) },
			events:  []Event{TestRefBuildFailed{BatchID: "b1"}},
			entries: []int{1}, batch: "b2", phase: Building,
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base1:[{1 head1}]"},
		},
		{
			name: "dequeue building", initial: func() State { return queued(1) },
			events: []Event{DequeueRequested{PR: 1}}, entries: []int{1}, batch: "b1", phase: Building,
			want: []string{"dequeue:1:requested", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "dequeue testing", initial: testingState,
			events: []Event{DequeueRequested{PR: 1}}, entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"dequeue:1:requested", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "head push while testing", initial: testingState,
			events:  []Event{PRObserved{PR: 1, HeadSHA: "new-head", Eligible: true, Trusted: true}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"dequeue:1:head-changed", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "eligibility revoked", initial: testingState,
			events:  []Event{PRObserved{PR: 1, HeadSHA: "head1", Trusted: true}},
			entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"dequeue:1:not-eligible", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "trust revoked", initial: func() State { return queued(1) },
			events:  []Event{PRObserved{PR: 1, HeadSHA: "head1", Eligible: true}},
			entries: []int{1}, batch: "b1", phase: Building,
			want: []string{"dequeue:1:untrusted", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "closed PR", initial: testingState,
			events: []Event{PRClosed{PR: 1}}, entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"dequeue:1:closed", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "waiting PR invalidated without cancelling front", initial: func() State { return queued(1, 2) },
			events:  []Event{PRObserved{PR: 2, HeadSHA: "new-head", Eligible: true, Trusted: true}},
			entries: []int{1, 2}, batch: "b1", phase: Building,
			want: []string{"dequeue:2:head-changed", "build:b1:base1:[{1 head1}]"},
		},
		{
			name: "base moves during building", initial: func() State { return queued(1) },
			events: []Event{BaseMoved{SHA: "base2"}}, entries: []int{1}, batch: "b2", phase: Building, base: "base2",
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base2:[{1 head1}]"},
		},
		{
			name: "base moves during testing", initial: testingState,
			events: []Event{BaseMoved{SHA: "base2"}}, entries: []int{1}, batch: "b2", phase: Building, base: "base2",
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base2:[{1 head1}]"},
		},
		{
			name: "unchanged base preserves testing", initial: testingState,
			events: []Event{BaseMoved{SHA: "base1"}}, entries: []int{1}, batch: "b1", phase: Testing,
			want: []string{"refresh:b1:tested1"},
		},
		{
			name: "base moves during landing", initial: landingState,
			events: []Event{BaseMoved{SHA: "base2"}}, entries: []int{1}, batch: "b1", phase: Landing, base: "base2",
			checkState: func(t *testing.T, s State) {
				if s.Active.BaseSHA != "base1" || s.Active.LandingAttempt != (LandingAttempt{ID: "attempt1", FenceToken: "f0", StartedAt: epoch}) {
					t.Fatal("in-flight expected base, fence, or timestamp changed")
				}
			},
		},
		{
			name: "dequeue during landing waits", initial: landingState,
			events: []Event{DequeueRequested{PR: 1}}, entries: []int{1}, batch: "b1", phase: Landing,
			checkState: func(t *testing.T, s State) {
				if !s.Entries[0].DequeueRequested {
					t.Fatal("deferred dequeue not recorded")
				}
			},
		},
		{
			name: "dequeue during landing already landed", initial: landingState,
			events: []Event{DequeueRequested{PR: 1}, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"}},
			base:   "tested1", fence: "f1",
			want: []string{"report:1:merged:already landed", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "dequeue during landing rejected", initial: landingState,
			events:  []Event{DequeueRequested{PR: 1}, BaseMoved{SHA: "base2"}, LandingAbandoned{BatchID: "b1", NewFence: "f1"}},
			entries: []int{1}, base: "base2", fence: "f1",
			want: []string{"dequeue:1:requested", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "dequeue during landing abandoned", initial: landingState,
			events:  []Event{DequeueRequested{PR: 1}, LandingAbandoned{BatchID: "b1", NewFence: "f1"}},
			entries: []int{1}, fence: "f1",
			want: []string{"dequeue:1:requested", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "landing rejection rebuilds without dequeue", initial: landingState,
			events:  []Event{BaseMoved{SHA: "base2"}, LandingAbandoned{BatchID: "b1", NewFence: "f1"}},
			entries: []int{1}, batch: "b2", phase: Building, base: "base2", fence: "f1",
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base2:[{1 head1}]"},
		},
		{
			name: "landing abandonment rebuilds on latest observed base", initial: landingState,
			events:  []Event{BaseMoved{SHA: "base2"}, LandingAbandoned{BatchID: "b1", NewFence: "f1"}},
			entries: []int{1}, batch: "b2", phase: Building, base: "base2", fence: "f1",
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base2:[{1 head1}]"},
		},
		{
			name: "stale build result and checks after base move", initial: testingState,
			events:  []Event{BaseMoved{SHA: "base2"}, TestRefBuilt{Method: Merge, BatchID: "b1", TestedSHA: "old", At: epoch}, ChecksCompleted{TestedSHA: "tested1", Success: true}},
			entries: []int{1}, batch: "b2", phase: Building, base: "base2",
			want: []string{"delete:openmerge/queue/main/b1", "build:b2:base2:[{1 head1}]"},
		},
		{
			name: "old SHA checks cannot settle rebuilt testing batch", initial: testingState,
			events:  []Event{BaseMoved{SHA: "base2"}, TestRefBuilt{Method: Merge, BatchID: "b2", TestedSHA: "tested2", At: epoch}, ChecksCompleted{TestedSHA: "tested1", Success: true}, ChecksCompleted{TestedSHA: "tested1"}},
			entries: []int{1}, batch: "b2", phase: Testing, base: "base2",
			want: []string{"delete:openmerge/queue/main/b1", "refresh:b2:tested2"},
		},
		{
			name: "explicit command supersedes deferred invalidation", initial: landingState,
			events:  []Event{PRClosed{PR: 1}, DequeueRequested{PR: 1}, LandingAbandoned{BatchID: "b1", NewFence: "f1"}},
			entries: []int{1}, fence: "f1",
			want: []string{"dequeue:1:requested", "delete:openmerge/queue/main/b1"},
		},
		{
			name: "durable landing record suppresses earlier cancellation", initial: readyState,
			events:  []Event{DequeueRequested{PR: 1}, LandingStarted{BatchID: "b1", AttemptID: "attempt1", FenceToken: "f0", At: epoch}},
			entries: []int{1}, batch: "b1", phase: Landing,
		},
		{
			name: "earlier cancellation still receives true landing outcome", initial: readyState,
			events: []Event{DequeueRequested{PR: 1}, LandingStarted{BatchID: "b1", AttemptID: "attempt1", FenceToken: "f0", At: epoch}, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"}},
			base:   "tested1", fence: "f1",
			want: []string{"delete:openmerge/queue/main/b1", "report:1:merged:already landed"},
		},
		{
			name: "dequeuing result advances queue", initial: func() State { return queued(1, 2) },
			events:  []Event{DequeueRequested{PR: 1}, Dequeued{PR: 1, AdmissionID: 1, Reason: Requested}},
			entries: []int{2}, batch: "b2", phase: Building,
			want: []string{"delete:openmerge/queue/main/b1", "report:1:dequeued:requested", "build:b2:base1:[{2 head2}]"},
		},
		{
			name: "late dequeued result cannot cancel landing", initial: landingState,
			events: []Event{Dequeued{PR: 1, AdmissionID: 1, Reason: ChecksTimeout}}, entries: []int{1}, batch: "b1", phase: Landing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := tt.initial()
			for _, event := range tt.events {
				s = applySerial(s, event)
			}
			var prs []int
			for _, entry := range s.Entries {
				prs = append(prs, entry.PR)
			}
			if !slices.Equal(prs, tt.entries) {
				t.Fatalf("entries = %v, want %v", prs, tt.entries)
			}
			if tt.batch == "" {
				if s.Active != nil {
					t.Fatalf("unexpected active batch: %+v", s.Active)
				}
			} else if s.Active == nil || s.Active.ID != tt.batch || s.Active.Phase != tt.phase {
				t.Fatalf("batch = %+v, want %s in %s", s.Active, tt.batch, tt.phase)
			}
			base, fence := tt.base, tt.fence
			if base == "" {
				base = "base1"
			}
			if fence == "" {
				fence = "f0"
			}
			if s.BaseSHA != base || s.Fence != fence {
				t.Fatalf("base/fence = %s/%s, want %s/%s", s.BaseSHA, s.Fence, base, fence)
			}
			assertPlan(t, testConfig, s, epoch.Add(30*time.Second), tt.want...)
			if tt.checkState != nil {
				tt.checkState(t, s)
			}
		})
	}
}

func TestPlanTimers(t *testing.T) {
	tests := []struct {
		name string
		s    State
		cfg  Config
		age  time.Duration
		want []string
	}{
		{"checks before deadline", testingState(), testConfig, 9 * time.Minute, []string{"refresh:b1:tested1"}},
		{"checks at deadline", testingState(), testConfig, 10 * time.Minute, []string{"refresh:b1:tested1"}},
		{"checks after deadline need refresh", testingState(), testConfig, 10*time.Minute + time.Nanosecond, []string{"refresh:b1:tested1"}},
		{"checks after deadline and refresh", applySerial(testingState(), ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: epoch.Add(10 * time.Minute)}), testConfig, 10*time.Minute + time.Nanosecond, []string{"dequeue:1:checks-timeout", "delete:openmerge/queue/main/b1"}},
		{"checks unlimited", testingState(), Config{}, 100 * time.Hour, []string{"refresh:b1:tested1"}},
		{"successful checks do not time out", readyState(), testConfig, time.Hour, []string{"land:b1:base1:tested1:f0"}},
		{"landing before delay", landingState(), testConfig, 59 * time.Second, nil},
		{"landing at delay", landingState(), testConfig, time.Minute, nil},
		{"landing after delay", landingState(), testConfig, time.Minute + time.Nanosecond, []string{"resolve:b1:attempt1"}},
		{"landing far past checks timeout", landingState(), testConfig, time.Hour, []string{"resolve:b1:attempt1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertPlan(t, tt.cfg, tt.s, epoch.Add(tt.age), tt.want...)
		})
	}
}

func TestChangesDuringLanding(t *testing.T) {
	tests := []struct {
		name   string
		event  Event
		reason DequeueReason
	}{
		{"head push", PRObserved{PR: 1, HeadSHA: "new", Eligible: true, Trusted: true}, HeadChanged},
		{"not eligible", PRObserved{PR: 1, HeadSHA: "head1", Trusted: true}, NotEligible},
		{"untrusted", PRObserved{PR: 1, HeadSHA: "head1", Eligible: true}, Untrusted},
		{"closed", PRClosed{PR: 1}, Closed},
	}
	for _, tt := range tests {
		for _, landed := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/landed=%t", tt.name, landed), func(t *testing.T) {
				t.Parallel()
				s := applySerial(landingState(), tt.event)
				assertPlan(t, testConfig, s, epoch.Add(time.Hour), "resolve:b1:attempt1")
				if landed {
					s = applySerial(s, Landed{BatchID: "b1", NewBaseSHA: "tested1", NewFence: "f1"})
					assertPlan(t, testConfig, s, epoch, "report:1:merged:already landed", "delete:openmerge/queue/main/b1")
				} else {
					s = applySerial(applySerial(s, BaseMoved{SHA: "base2"}), LandingAbandoned{BatchID: "b1", NewFence: "f1"})
					assertPlan(t, testConfig, s, epoch, "dequeue:1:"+string(tt.reason), "delete:openmerge/queue/main/b1")
				}
			})
		}
	}
}

func TestEffectAcknowledgements(t *testing.T) {
	t.Run("report repeats until acknowledged and never returns", func(t *testing.T) {
		s := applySerial(queued(), EnqueueRequested{PR: 1, At: epoch})
		actions := assertPlan(t, testConfig, s, epoch, "report:1:blocked:PR does not satisfy queue entry conditions")
		assertPlan(t, testConfig, s, epoch.Add(time.Hour), descriptions(actions)...)
		s = applySerial(s, ActionCompleted{Key: actions[0].Key()})
		assertPlan(t, testConfig, s, epoch)
		s = applySerial(s, BaseMoved{SHA: "base2"})
		assertPlan(t, testConfig, s, epoch)
	})
	t.Run("delete acknowledgement survives until dequeue result", func(t *testing.T) {
		s := testingState()
		now := epoch.Add(time.Hour)
		s = applySerial(s, ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: now})
		actions := assertPlan(t, testConfig, s, now, "dequeue:1:checks-timeout", "delete:openmerge/queue/main/b1")
		s = applySerial(s, ActionCompleted{Key: actions[1].Key()})
		remaining := assertPlan(t, testConfig, s, now, "dequeue:1:checks-timeout")
		if remaining[0].Key() != actions[0].Key() {
			t.Fatal("unrelated acknowledgement changed timeout dequeue key")
		}
		s = applySerial(s, Dequeued{PR: 1, AdmissionID: 1, Reason: ChecksTimeout})
		assertPlan(t, testConfig, s, now, "report:1:dequeued:checks-timeout")
	})
	t.Run("generic acknowledgement cannot complete a dequeue", func(t *testing.T) {
		s := applySerial(queued(1), DequeueRequested{PR: 1})
		actions := Plan(testConfig, s, epoch)
		s = applySerial(s, ActionCompleted{Key: actions[0].Key()})
		assertPlan(t, testConfig, s, epoch, "dequeue:1:requested", "delete:openmerge/queue/main/b1")
	})
	t.Run("PR acknowledgement does not resurrect after the entry leaves", func(t *testing.T) {
		s := applySerial(queued(), EnqueueRequested{PR: 1, At: epoch})
		report := Plan(testConfig, s, epoch)[0]
		s = applySerial(s, PRObserved{PR: 1, HeadSHA: "head1", Eligible: true, Trusted: true})
		s = applySerial(s, EnqueueRequested{PR: 1, At: epoch})
		s = applySerial(s, ActionCompleted{Key: report.Key()})
		s = applySerial(s, DequeueRequested{PR: 1})
		s = applySerial(s, Dequeued{PR: 1, AdmissionID: 1, Reason: Requested})
		for _, action := range Plan(testConfig, s, epoch) {
			if action.Key() == report.Key() {
				t.Fatal("pruning resurrected acknowledged report")
			}
		}
	})
}

func TestIgnoredResultEvents(t *testing.T) {
	events := []Event{
		TestRefBuilt{Method: Merge, BatchID: "old", TestedSHA: "old", At: epoch},
		TestRefBuildFailed{BatchID: "old", Conflict: true},
		LandingStarted{FenceToken: "fence", BatchID: "old", AttemptID: "old", At: epoch},
		Landed{BatchID: "old", NewBaseSHA: "wrong", NewFence: "wrong"},
		LandingAbandoned{BatchID: "old", NewFence: "wrong"},
	}
	for _, initial := range []func() State{testingState, landingState} {
		for _, event := range events {
			t.Run(fmt.Sprintf("%s/%T", initial().Active.Phase, event), func(t *testing.T) {
				s := initial()
				before := Plan(testConfig, s, epoch)
				s = applySerial(s, event)
				if s.BaseSHA != "base1" || s.Fence != "f0" || !reflect.DeepEqual(before, Plan(testConfig, s, epoch)) {
					t.Fatal("stale result changed the queue")
				}
			})
		}
	}
}

func TestActionKeysAcrossQueueLifetimes(t *testing.T) {
	s := applySerial(queued(1), DequeueRequested{PR: 1})
	first := Plan(testConfig, s, epoch)[0]
	s = applySerial(s, DequeueRequested{PR: 1})
	if Plan(testConfig, s, epoch)[0].Key() != first.Key() {
		t.Fatal("duplicate dequeue command changed the pending action key")
	}
	s = applySerial(s, Dequeued{PR: 1, AdmissionID: 1, Reason: Requested})
	oldReport := Plan(testConfig, s, epoch)[1]
	s = applySerial(s, EnqueueRequested{PR: 1, At: epoch})
	s = applySerial(s, DequeueRequested{PR: 1})
	second := Plan(testConfig, s, epoch)[0]
	if first.Key() == second.Key() {
		t.Fatal("later enqueue reused an old dequeue key")
	}
	s = applySerial(s, Dequeued{PR: 1, AdmissionID: second.(Dequeue).AdmissionID, Reason: Requested})
	actions := Plan(testConfig, s, epoch)
	newReport := actions[len(actions)-1]
	if oldReport.Key() == newReport.Key() {
		t.Fatal("later dequeue reused an old report key")
	}
}

func TestActionPayloads(t *testing.T) {
	build := Plan(testConfig, queued(1), epoch)[0].(BuildTestRef)
	if build.BatchID != "b1" || build.BaseSHA != "base1" || build.Ref != "openmerge/queue/main/b1" || !slices.Equal(build.PRs, []BatchPR{{PR: 1, HeadSHA: "head1"}}) {
		t.Fatalf("incorrect build payload: %+v", build)
	}
	land := Plan(testConfig, readyState(), epoch)[0].(Land)
	if land.BatchID != "b1" || land.ExpectedBaseSHA != "base1" || land.TestedSHA != "tested1" || land.Ref != build.Ref || land.FenceToken != "f0" {
		t.Fatalf("incorrect landing payload: %+v", land)
	}
}

func TestApplyDoesNotMutateInput(t *testing.T) {
	events := []Event{
		PRObserved{PR: 1, HeadSHA: "new", Eligible: true, Trusted: true},
		PRObserved{PR: 3, HeadSHA: "head3", Eligible: true, Trusted: true},
		EnqueueRequested{PR: 2, At: epoch}, DequeueRequested{PR: 1}, PRClosed{PR: 1}, BaseMoved{SHA: "base2"}, ResumeRequested{},
		TestRefBuilt{Method: Merge, BatchID: "b1", TestedSHA: "test", At: epoch}, TestRefBuildFailed{BatchID: "b1", Conflict: true},
		TestRefBuildFailed{BatchID: "b1", At: epoch},
		ChecksCompleted{TestedSHA: "tested1"}, LandingStarted{FenceToken: "fence", BatchID: "b1", AttemptID: "a2", At: epoch},
		ChecksRefreshed{BatchID: "b1", TestedSHA: "tested1", At: epoch},
		Landed{BatchID: "b1", NewBaseSHA: "test", NewFence: "f1"},
		BaseMoved{SHA: "base2"}, LandingAbandoned{BatchID: "b1", NewFence: "f1"},
		LandingAbandoned{BatchID: "b1", NewFence: "f1"}, Dequeued{PR: 1, AdmissionID: 1, Reason: Requested},
		ActionCompleted{Key: (DeleteRef{Ref: "openmerge/queue/main/b1"}).Key()},
	}
	for _, initial := range []func() State{
		func() State { return queued(1) }, testingState, landingState,
		func() State { return applySerial(testingState(), DequeueRequested{PR: 1}) },
		func() State { return applySerial(queued(1), ChecksCompleted{TestedSHA: "test", Success: true}) },
	} {
		for _, event := range events {
			t.Run(fmt.Sprintf("%s/%T", initial().Active.Phase, event), func(t *testing.T) {
				s, original := initial(), initial()
				next := applySerial(s, event)
				if !reflect.DeepEqual(s, original) {
					t.Fatal("Apply mutated input")
				}
				if len(next.Entries) > 0 {
					next.Entries[0].HeadSHA = "caller mutation"
				}
				if next.Active != nil {
					next.Active.PRs[0].HeadSHA = "caller mutation"
				}
				if len(next.effects) > 0 {
					next.effects[0] = DeleteRef{Ref: "caller mutation"}
				}
				next.observations[1] = PRObserved{}
				next.dequeues[1] = Dequeue{}
				next.checks["caller mutation"] = retainedCheck{}
				next.testedBatches["caller mutation"] = "b99"
				next.acknowledged["caller mutation"] = effectScope{}
				if !reflect.DeepEqual(s, original) {
					t.Fatal("returned state aliases input")
				}
			})
		}
	}
}

func TestPlanDoesNotExposeStateSlices(t *testing.T) {
	s, original := queued(1), queued(1)
	actions := Plan(testConfig, s, epoch)
	actions[0].(BuildTestRef).PRs[0].HeadSHA = "caller mutation"
	if !reflect.DeepEqual(s, original) {
		t.Fatal("Plan exposed state through an action")
	}
}

type scenario struct {
	t   *testing.T
	s   State
	now time.Time
}

func (sc *scenario) step(event Event, want ...string) {
	sc.t.Helper()
	sc.s = applySerial(sc.s, event)
	assertPlan(sc.t, testConfig, sc.s, sc.now, want...)
}

func (sc *scenario) acknowledgeEffects() {
	sc.t.Helper()
	for _, action := range Plan(testConfig, sc.s, sc.now) {
		switch action.(type) {
		case ReportStatus, DeleteRef:
			sc.s = applySerial(sc.s, ActionCompleted{Key: action.Key()})
		}
	}
}

func (sc *scenario) land(batch, tested, base, fence, nextFence string, next ...string) {
	sc.t.Helper()
	sc.step(TestRefBuilt{Method: Merge, BatchID: batch, TestedSHA: tested, At: sc.now}, "refresh:"+batch+":"+tested)
	sc.step(ChecksCompleted{TestedSHA: tested, Success: true}, "land:"+batch+":"+base+":"+tested+":"+fence)
	sc.step(LandingStarted{BatchID: batch, AttemptID: "attempt-" + batch, FenceToken: fence, At: sc.now})
	pr := sc.s.Active.PRs[0].PR
	want := []string{fmt.Sprintf("report:%d:merged:PR landed", pr), "delete:openmerge/queue/main/" + batch}
	want = append(want, next...)
	sc.step(Landed{BatchID: batch, NewBaseSHA: tested, NewFence: nextFence}, want...)
	sc.acknowledgeEffects()
}

func TestSerialScenarios(t *testing.T) {
	t.Run("three PRs land in FIFO order", func(t *testing.T) {
		sc := scenario{t: t, s: queued(1, 2, 3), now: epoch}
		sc.land("b1", "tested1", "base1", "f0", "f1", "build:b2:tested1:[{2 head2}]")
		sc.land("b2", "tested2", "tested1", "f1", "f2", "build:b3:tested2:[{3 head3}]")
		sc.land("b3", "tested3", "tested2", "f2", "f3")
		if len(sc.s.Entries) != 0 || sc.s.Active != nil || sc.s.BaseSHA != "tested3" || sc.s.Fence != "f3" {
			t.Fatalf("unexpected final state: %+v", sc.s)
		}
		assertPlan(t, testConfig, sc.s, epoch)
	})
	t.Run("middle PR fails checks", func(t *testing.T) {
		sc := scenario{t: t, s: queued(1, 2, 3), now: epoch}
		sc.land("b1", "tested1", "base1", "f0", "f1", "build:b2:tested1:[{2 head2}]")
		sc.step(TestRefBuilt{Method: Merge, BatchID: "b2", TestedSHA: "bad", At: epoch}, "refresh:b2:bad")
		sc.step(ChecksCompleted{TestedSHA: "bad"}, "dequeue:2:checks-failed", "delete:openmerge/queue/main/b2")
		sc.acknowledgeEffects()
		sc.step(Dequeued{PR: 2, AdmissionID: 2, Reason: ChecksFailed}, "report:2:dequeued:checks-failed", "build:b3:tested1:[{3 head3}]")
		sc.acknowledgeEffects()
		sc.land("b3", "tested3", "tested1", "f1", "f2")
		if len(sc.s.Entries) != 0 || sc.s.BaseSHA != "tested3" {
			t.Fatalf("unexpected final state: %+v", sc.s)
		}
	})
	t.Run("landing rejected then succeeds", func(t *testing.T) {
		sc := scenario{t: t, s: landingState(), now: epoch}
		sc.s = applySerial(sc.s, BaseMoved{SHA: "external"})
		sc.step(LandingAbandoned{BatchID: "b1", NewFence: "f1"}, "delete:openmerge/queue/main/b1", "build:b2:external:[{1 head1}]")
		sc.acknowledgeEffects()
		sc.step(ChecksCompleted{TestedSHA: "tested1", Success: true}, "build:b2:external:[{1 head1}]")
		sc.land("b2", "rebuilt", "external", "f1", "f2")
		if len(sc.s.Entries) != 0 || sc.s.BaseSHA != "rebuilt" || sc.s.Fence != "f2" {
			t.Fatalf("unexpected final state: %+v", sc.s)
		}
	})
	t.Run("timeout dequeue advances to next PR", func(t *testing.T) {
		sc := scenario{t: t, s: queued(1, 2), now: epoch}
		sc.step(TestRefBuilt{Method: Merge, BatchID: "b1", TestedSHA: "slow", At: epoch}, "refresh:b1:slow")
		sc.now = epoch.Add(11 * time.Minute)
		assertPlan(t, testConfig, sc.s, sc.now, "refresh:b1:slow")
		sc.step(ChecksRefreshed{BatchID: "b1", TestedSHA: "slow", At: sc.now}, "dequeue:1:checks-timeout", "delete:openmerge/queue/main/b1")
		sc.step(Dequeued{PR: 1, AdmissionID: 1, Reason: ChecksTimeout}, "delete:openmerge/queue/main/b1", "report:1:dequeued:checks-timeout", "build:b2:base1:[{2 head2}]")
		sc.acknowledgeEffects()
		sc.land("b2", "tested2", "base1", "f0", "f1")
		if len(sc.s.Entries) != 0 || sc.s.BaseSHA != "tested2" {
			t.Fatalf("unexpected final state: %+v", sc.s)
		}
	})
}

// applySerial records the planner's size-one composition in the legacy serial
// scenario harness. Production Apply only creates batches from BatchStarted.
func applySerial(s State, event Event) State {
	s = Apply(s, event)
	if s.Active == nil && len(s.Entries) > 0 {
		entry := s.Entries[0]
		prs := []BatchPR{{PR: entry.PR, HeadSHA: entry.HeadSHA}}
		if s.runnablePRs(prs) {
			s = Apply(s, BatchStarted{BatchID: s.nextBatchID(), BaseSHA: s.BaseSHA, PRs: prs, At: epoch})
		}
	}
	return s
}
