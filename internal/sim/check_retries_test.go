package sim

import (
	"container/heap"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func retryConfig() Config {
	cfg := smallConfig()
	cfg.PRs = 1
	cfg.CI = &CIConfig{Runners: 1, CancellationSupported: true, RerunsSupported: true}
	cfg.Planner.ChecksRetryFailed = 1
	cfg.Planner.CheckTimeout = 30 * time.Minute
	cfg.Faults = Faults{}
	return cfg
}

func advanceUntilEvent(t *testing.T, s *simulation, want func(queue.Event) bool) queue.Event {
	t.Helper()
	for i := 0; i < 10_000; i++ {
		if _, err := s.step(); err != nil {
			t.Fatalf("simulation step %d: %v", i, err)
		}
		if len(s.events) > 0 {
			event := s.events[len(s.events)-1].Event
			if want(event) {
				return event
			}
		}
	}
	t.Fatal("wanted simulator event was not emitted")
	return nil
}

func TestRetryFlakyFirstRunThenPassLands(t *testing.T) {
	cfg := retryConfig()
	cfg.Seed, cfg.FlakeRate = 6, .5
	s, result := runScenario(t, cfg)
	if result.Merged != 1 || result.CIRuns != 2 || result.CIReruns != 1 || len(result.Dequeued) != 0 {
		t.Fatalf("flaky retry did not land: %+v", result)
	}

	var observations []queue.ChecksObserved
	var legacyCompleted int
	var requested, dispatched int
	for _, entry := range s.events {
		switch event := entry.Event.(type) {
		case queue.ChecksObserved:
			if event.Terminal {
				observations = append(observations, event)
			}
		case queue.ChecksCompleted:
			legacyCompleted++
		case queue.CheckRerunRequested:
			requested++
		case queue.CheckRerunDispatched:
			dispatched++
		}
	}
	if len(observations) != 2 || requested != 1 || dispatched != 1 || legacyCompleted != 0 {
		t.Fatalf("unexpected retry event flow: terminal observations=%d requested=%d dispatched=%d legacy completions=%d", len(observations), requested, dispatched, legacyCompleted)
	}
	if observations[0].Success || !observations[0].Terminal || observations[1].Terminal == false || !observations[1].Success {
		t.Fatalf("unexpected flaky observations: %+v", observations)
	}
	if len(observations[0].Runs) != 1 || len(observations[1].Runs) != 1 {
		t.Fatalf("unexpected check identities: %+v", observations)
	}
	first, second := observations[0].Runs[0], observations[1].Runs[0]
	if first.ID != 1 || first.Attempt != 1 || first.Source != "fake:ci" || first.State != "failure" {
		t.Fatalf("unexpected original check identity: %+v", first)
	}
	if second.ID != 2 || second.Attempt != 2 || second.Source != "fake:ci" || second.State != "success" {
		t.Fatalf("unexpected rerun check identity: %+v", second)
	}
	run := s.tests[observations[0].TestedSHA]
	if !observations[0].OriginalDeadline.Equal(run.startedTestingAt.Add(cfg.Planner.CheckTimeout)) || observations[0].Timeout != cfg.Planner.CheckTimeout {
		t.Fatalf("retry timer was not bound to testing start: observation=%+v run=%+v", observations[0], run)
	}
}

func TestRetryBrokenBatchSplitsAndDequeues(t *testing.T) {
	cfg := retryConfig()
	cfg.PRs, cfg.BrokenRate = 2, 1
	cfg.Planner.BatchMaxSize = 2
	cfg.Planner.BatchMaxWait = time.Second
	s, result := runScenario(t, cfg)
	if result.Merged != 0 || result.Dequeued[queue.ChecksFailed] != 2 || result.CIRuns != 6 || result.CIReruns != 3 {
		t.Fatalf("broken retry batch did not split and drain: %+v", result)
	}
	confirmations, starts := 0, make(map[int]int)
	for _, entry := range s.events {
		switch event := entry.Event.(type) {
		case queue.ChecksFailureConfirmed:
			confirmations++
		case queue.BatchStarted:
			starts[len(event.PRs)]++
		}
	}
	if confirmations != 3 || starts[2] != 1 || starts[1] != 2 {
		t.Fatalf("broken retries did not preserve bisection flow: confirmations=%d starts=%v", confirmations, starts)
	}
}

func TestRetryDuplicateOriginalObservationDoesNotConfirm(t *testing.T) {
	cfg := retryConfig()
	cfg.FlakeRate = 1
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	event := advanceUntilEvent(t, s, func(event queue.Event) bool {
		observation, ok := event.(queue.ChecksObserved)
		return ok && observation.Terminal && !observation.Success
	})
	original := event.(queue.ChecksObserved)
	if s.state.Active == nil || s.state.Active.CheckRetry == nil {
		t.Fatalf("failed observation did not create retry state: %+v", s.state.Active)
	}
	s.record(original)
	s.record(original)
	if s.state.Active.CheckRetry.Confirmed {
		t.Fatal("duplicate original observations confirmed the failure")
	}
	actions, err := s.plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) == 0 {
		t.Fatal("duplicate observation suppressed the retry plan")
	}
	if _, ok := actions[0].(queue.RecordCheckRerun); !ok {
		t.Fatalf("duplicate observation changed retry plan to %T", actions[0])
	}
	for _, entry := range s.events {
		if _, ok := entry.Event.(queue.ChecksFailureConfirmed); ok {
			t.Fatal("duplicate original observation emitted failure confirmation")
		}
	}
}

func TestRetryReopenDuringRerunStillAllowsFreshPass(t *testing.T) {
	cfg := retryConfig()
	cfg.Seed, cfg.FlakeRate = 6, .5
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	event := advanceUntilEvent(t, s, func(event queue.Event) bool {
		_, ok := event.(queue.CheckRerunDispatched)
		return ok
	})
	_ = event
	if s.state.Active == nil || s.state.Active.CheckRetry == nil || !s.state.Active.CheckRetry.Dispatched {
		t.Fatalf("rerun was not durable before reopen: %+v", s.state.Active)
	}
	started, deadline := s.state.Active.StartedTestingAt, s.state.Active.CheckRetry.OriginalDeadline
	sha := s.state.Active.TestedSHA
	s.record(queue.ChecksReopened{TestedSHA: sha, At: s.now})
	if s.state.Active.CheckRetry == nil || s.state.Active.StartedTestingAt != started || !s.state.Active.CheckRetry.OriginalDeadline.Equal(deadline) {
		t.Fatalf("reopen changed retry identity or original deadline: %+v", s.state.Active)
	}
	for _, entry := range s.events {
		if _, ok := entry.Event.(queue.ChecksFailureConfirmed); ok {
			t.Fatal("reopen during rerun confirmed failure before the rerun completed")
		}
	}
	result, err := s.run()
	if err != nil || result.Merged != 1 || result.Dequeued[queue.ChecksFailed] != 0 {
		t.Fatalf("reopen during rerun lost fresh pass: %+v, %v", result, err)
	}
}

func TestRetryTimeoutConfirmsAfterFreshPendingObservation(t *testing.T) {
	cfg := retryConfig()
	cfg.FlakeRate = 1
	cfg.CIDuration = 2 * time.Minute
	cfg.Planner.CheckTimeout = 30 * time.Second
	cfg.Planner.RefreshInterval = time.Hour
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var pending, timeoutConfirmed bool
	var timeoutAction queue.ConfirmCheckFailure
	for i := 0; i < 10_000; i++ {
		actions, planErr := s.plan()
		if planErr != nil {
			t.Fatal(planErr)
		}
		for _, action := range actions {
			if confirm, ok := action.(queue.ConfirmCheckFailure); ok && confirm.Reason == queue.ChecksTimeout {
				timeoutAction, timeoutConfirmed = confirm, true
				break
			}
		}
		if timeoutConfirmed {
			break
		}
		if _, err := s.step(); err != nil {
			t.Fatalf("simulation step %d: %v", i, err)
		}
	}
	for _, entry := range s.events {
		if event, ok := entry.Event.(queue.ChecksObserved); ok {
			pending = pending || !event.Terminal
		}
	}
	if !pending || !timeoutConfirmed || timeoutAction.BatchID == "" {
		t.Fatalf("timeout lacked fresh pending evidence or confirmation plan: pending=%v confirmed=%v action=%+v", pending, timeoutConfirmed, timeoutAction)
	}
	result, err := s.run()
	if err != nil || result.Dequeued[queue.ChecksTimeout] != 1 || result.CIReruns != 0 || result.Merged != 0 {
		t.Fatalf("original timeout did not drain without retry: %+v, %v", result, err)
	}
}

func TestRetryUnsupportedProviderConfirmsFirstFailure(t *testing.T) {
	cfg := retryConfig()
	cfg.BrokenRate = 1
	cfg.CI.RerunsSupported = false
	s, result := runScenario(t, cfg)
	if result.Merged != 0 || result.Dequeued[queue.ChecksFailed] != 1 || result.CIRuns != 1 || result.CIReruns != 0 {
		t.Fatalf("unsupported retry capability changed failure outcome: %+v", result)
	}
	var unsupported, confirmed bool
	for _, entry := range s.events {
		switch event := entry.Event.(type) {
		case queue.CheckRerunDispatched:
			unsupported = unsupported || event.Unsupported
		case queue.ChecksFailureConfirmed:
			confirmed = confirmed || event.Reason == queue.ChecksFailed
		}
	}
	if !unsupported || !confirmed {
		t.Fatalf("unsupported capability was not durably handled: unsupported=%v confirmed=%v", unsupported, confirmed)
	}
}

func TestRetryFaultMethodIsOptInAndNotInLegacyDefaults(t *testing.T) {
	if _, ok := DefaultFaults().Methods["RerunFailedChecks"]; ok {
		t.Fatal("legacy DefaultFaults unexpectedly includes optional retry capability")
	}
	if _, ok := DefaultFaults().FailFirst["RerunFailedChecks"]; ok {
		t.Fatal("legacy DefaultFaults unexpectedly includes optional retry capability fault")
	}
	cfg := retryConfig()
	cfg.PRs = 0
	cfg.Faults.Methods = map[string]float64{"RerunFailedChecks": 0}
	if _, err := Run(cfg); err != nil {
		t.Fatalf("retry-enabled optional fault method rejected: %v", err)
	}
	cfg.Planner.ChecksRetryFailed = 0
	if _, err := Run(cfg); err == nil {
		t.Fatal("retry fault method accepted while retry support was disabled")
	}
}

func TestRequestedRerunGetsOwnTimeoutAndDequeues(t *testing.T) {
	cfg := retryConfig()
	cfg.FlakeRate = 1
	cfg.CIDuration = time.Minute
	cfg.Planner.CheckTimeout = 3 * time.Minute
	cfg.Planner.RefreshInterval = time.Hour
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	advanceUntilEvent(t, s, func(event queue.Event) bool { _, ok := event.(queue.CheckRerunDispatched); return ok })
	retry := s.state.Active.CheckRetry
	sha := s.state.Active.TestedSHA
	// Model a rerun that stays on the runner past its independently recorded deadline.
	delayed := false
	for i := range s.scheduled {
		event := &s.scheduled[i]
		if event.kind == ciComplete && event.sha == sha && event.job == s.tests[sha].job {
			event.at = retry.Deadline.Add(time.Minute)
			event.job.duration = event.at.Sub(event.job.started)
			delayed = true
		}
	}
	if !delayed {
		t.Fatal("rerun completion was not scheduled")
	}
	heap.Init(&s.scheduled)
	result, err := s.run()
	if err != nil || result.Dequeued[queue.ChecksTimeout] != 1 || result.CIReruns != 1 || result.CIRuns != 2 || result.Merged != 0 {
		t.Fatalf("rerun timeout did not drain: %+v, %v", result, err)
	}
	var confirmationAt time.Time
	for _, entry := range s.events {
		if event, ok := entry.Event.(queue.ChecksFailureConfirmed); ok && event.Reason == queue.ChecksTimeout {
			confirmationAt = entry.At
		}
	}
	if !confirmationAt.After(retry.Deadline) {
		t.Fatalf("timeout ignored rerun deadline: confirmation=%v deadline=%v", confirmationAt, retry.Deadline)
	}
}
