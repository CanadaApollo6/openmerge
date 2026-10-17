package sim

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func cancellationSimulation(t *testing.T, supported bool) *simulation {
	t.Helper()
	cfg := smallConfig()
	cfg.PRs = 0
	cfg.CI = &CIConfig{Runners: 1, CancellationSupported: supported}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.tests["retired"] = &testRun{batchID: "retired", ref: "refs/retired", success: true}
	s.submitCI("retired", time.Hour)
	s.tests["other"] = &testRun{batchID: "other", ref: "refs/other", success: true}
	s.submitCI("other", time.Hour)
	return s
}

func cancellationRequest() forge.CancelChecksRequest {
	return forge.CancelChecksRequest{TestedSHA: "retired", TestRef: "refs/retired", Key: "cancel/retired", Runs: []forge.CheckRun{{ID: 1, Attempt: 1, Name: "ci", Source: "fake:ci", State: forge.Pending}}}
}

func TestCancellationSimulatorScopeAndIdempotency(t *testing.T) {
	for _, supported := range []bool{true, false} {
		s := cancellationSimulation(t, supported)
		s.now = s.now.Add(10 * time.Minute)
		for range 2 {
			result, err := s.boundary.CancelChecks(context.Background(), s.repo, cancellationRequest())
			if err != nil || (result.Unsupported != "") == supported || len(result.Confirmed) != map[bool]int{true: 1, false: 0}[supported] {
				t.Fatalf("unexpected cancellation result: %+v (%v)", result, err)
			}
		}
		retired, other := s.tests["retired"].job, s.tests["other"].job
		if retired.cancelled != supported || other.cancelled || s.ciRunning != 1 || s.ciMetrics(0).CancellationsRequested != 1 {
			t.Fatalf("cancellation repeated or broadened effect: retired=%+v other=%+v", retired, other)
		}
		if supported && (retired.finished.Sub(retired.started) != 10*time.Minute || other.started != s.now) {
			t.Fatal("cancellation did not free runner at the observed completion")
		}
		if !supported && !other.started.IsZero() {
			t.Fatal("unsupported cancellation freed runner capacity")
		}
	}
}

func TestCancellationSimulatorRejectsLiveAndLanded(t *testing.T) {
	for _, phase := range []string{"building", "testing", "landing", "landed"} {
		t.Run(phase, func(t *testing.T) {
			s := cancellationSimulation(t, true)
			if phase == "landed" {
				s.tests["retired"].landed = true
			} else {
				s.state.Active = &queue.Batch{ID: "retired", TestedSHA: "retired", Phase: queue.Phase(phase)}
			}
			_, err := s.boundary.CancelChecks(context.Background(), s.repo, cancellationRequest())
			if err == nil || !strings.Contains(err.Error(), "cancellation targeted") || s.tests["retired"].job.cancellationRequested {
				t.Fatalf("simulator missed unsafe cancellation: %v", err)
			}
		})
	}
}

func TestCancellationSimulatorDoesNotBroadenRunIdentity(t *testing.T) {
	for _, change := range []string{"sha", "ref", "id", "attempt", "name", "source"} {
		t.Run(change, func(t *testing.T) {
			s := cancellationSimulation(t, true)
			req := cancellationRequest()
			switch change {
			case "sha":
				req.TestedSHA = "other"
			case "ref":
				req.TestRef = "refs/other"
			case "id":
				req.Runs[0].ID++
			case "attempt":
				req.Runs[0].Attempt++
			case "name":
				req.Runs[0].Name = "unidentified"
			case "source":
				req.Runs[0].Source = "other"
			}
			result, _ := s.boundary.CancelChecks(context.Background(), s.repo, req)
			if len(result.Confirmed) > 0 || s.tests["retired"].job.cancelled || s.tests["other"].job.cancelled {
				t.Fatal("unattributable cancellation changed runner state")
			}
		})
	}
}

func TestSpeculativeCancellationRecoversAndNeverRevives(t *testing.T) {
	for _, supported := range []bool{true, false} {
		cfg := smallConfig()
		cfg.PRs, cfg.ArrivalWindow, cfg.BrokenRate = 24, 0, .2
		cfg.CIMin, cfg.CIMax = time.Minute, 3*time.Minute
		cfg.Planner.CheckTimeout = 10 * time.Minute
		cfg.Planner.SpeculationMaxDepth = 4
		cfg.CI = &CIConfig{Runners: 2, CancellationSupported: supported, RerunsSupported: true}
		cfg.Faults = Faults{FailFirst: map[string]int{"CancelChecks": 1}}
		s, r := runScenario(t, cfg)
		if r.Merged+sumDequeued(r) != cfg.PRs || r.FaultsByMethod["CancelChecks"] != 1 || r.FaultsRecovered != r.FaultsInjected || r.CI.CancellationsRequested == 0 {
			t.Fatalf("cancellation recovery did not drain: %+v; CI=%+v; pending=%v", r, r.CI, s.pendingFaults)
		}
		if supported && (r.CI.CancellationsConfirmed == 0 || r.CI.CancellationsUnsupported != 0) {
			t.Fatalf("supported cancellation did not release identified work: %+v", r.CI)
		}
		if !supported && (r.CI.CancellationsConfirmed != 0 || r.CI.CancellationsUnsupported == 0) {
			t.Fatalf("unsupported cancellation incorrectly stopped work: %+v", r.CI)
		}
	}
}

func TestCancellationCompletedAttemptAndWastedRerunAccounting(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 0
	cfg.CI = &CIConfig{CancellationSupported: true}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	run := &testRun{batchID: "retired", ref: "refs/retired"}
	s.tests["retired"] = run
	s.submitCI("retired", 20*time.Minute)
	s.now = s.now.Add(20 * time.Minute)
	run.job.finished = s.now
	run.done, run.success = true, true
	s.submitCI("retired", 20*time.Minute)
	s.now = s.now.Add(20 * time.Minute)
	run.job.finished, run.landed = s.now, true
	metrics := s.ciMetrics(1)
	if metrics.RunsStarted != 2 || metrics.RerunsStarted != 1 || metrics.WastedRuns != 1 || metrics.RunnerMinutes != 40 {
		t.Fatalf("rerun costs did not retain the failed attempt: %+v", metrics)
	}
	// Cancellation confirmation of an earlier attempt preserves that attempt's
	// actual failure, even when a later attempt of the same SHA succeeded.
	run.landed = false
	result, err := s.boundary.CancelChecks(context.Background(), s.repo, cancellationRequest())
	if err != nil || len(result.Confirmed) != 1 || result.Confirmed[0].State != forge.Failure || s.ciJobs[0].cancelled || run.job.cancelled {
		t.Fatalf("completed attempt was cancelled or lost its outcome: %+v (%v)", result, err)
	}
}

func TestUnsupportedRetiredChecksObserveLateOutcomeWithoutWebhook(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.ArrivalWindow, cfg.BrokenRate = 24, 0, .2
	cfg.CIMin, cfg.CIMax = time.Minute, 3*time.Minute
	cfg.Planner.CheckTimeout = 10 * time.Minute
	cfg.Planner.SpeculationMaxDepth = 4
	cfg.CI = &CIConfig{Runners: 2, RerunsSupported: true}
	cfg.DropWebhookRate = 1
	s, _ := runScenario(t, cfg)
	unsupported, observed := map[string]bool{}, map[string]bool{}
	for _, entry := range s.events {
		switch event := entry.Event.(type) {
		case queue.CheckCancellationReported:
			if event.Unsupported {
				unsupported[event.BatchID] = true
			}
		case queue.ChecksObserved:
			if unsupported[event.BatchID] && event.Terminal {
				observed[event.BatchID] = true
			}
		case queue.Landed:
			if unsupported[event.BatchID] {
				t.Fatal("historical check outcome revived an unsupported retired batch")
			}
		}
	}
	if len(unsupported) == 0 || len(observed) == 0 {
		t.Fatalf("unsupported retired work lacked late read-only completion evidence: unsupported=%v observed=%v", unsupported, observed)
	}
}

func TestRetiredObservationPreservesConfiguredFaults(t *testing.T) {
	for _, fault := range []bool{false, true} {
		s := cancellationSimulation(t, false)
		control := cancellationSimulation(t, false)
		s.boundary.retiredObservation = true
		if fault {
			s.cfg.Faults.FailFirst = map[string]int{"CheckRuns": 1}
		}
		_, err := s.boundary.CheckRuns(context.Background(), s.repo, "retired")
		if fault {
			if err != errForge || s.result.FaultsByMethod["CheckRuns"] != 1 {
				t.Fatalf("configured retired read fault was suppressed: %v", err)
			}
		} else if err != nil || s.faults.Uint64() != control.faults.Uint64() {
			t.Fatal("fault-free historical read changed the workload's operational RNG")
		}
	}
}
