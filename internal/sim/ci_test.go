package sim

import (
	"container/heap"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/sim/capacity"
)

func TestCIQueueServiceAndCancellation(t *testing.T) {
	for _, cancel := range []bool{true, false} {
		cfg := smallConfig()
		cfg.PRs = 0
		cfg.CI = &CIConfig{Runners: 1, CancellationSupported: cancel}
		s, err := newSimulation(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, sha := range []string{"one", "two"} {
			s.tests[sha] = &testRun{success: true}
			s.submitCI(sha, 3*time.Hour)
		}
		if s.result.CIRuns != 1 || len(s.ciWaiting) != 1 {
			t.Fatal("runner queue did not bind")
		}
		s.now = s.now.Add(time.Hour)
		s.cancelCI("one")
		s.cancelCI("one")
		for len(s.scheduled) > 0 {
			e := heap.Pop(&s.scheduled).(scheduledEvent)
			s.now = e.at
			if err := s.deliver(e); err != nil {
				t.Fatal(err)
			}
		}
		m := s.ciMetrics(0)
		want := 360.
		if cancel {
			want = 240
		}
		if m.RunsStarted != 2 || m.CancellationsRequested != 1 || m.RunnerMinutes != want {
			t.Fatalf("wrong accounting: %+v", m)
		}
		if got := s.tests["two"].job.finished.Sub(s.tests["two"].job.started); got != 3*time.Hour {
			t.Fatalf("queue wait consumed service: %s", got)
		}
	}
}
func TestFailedRerunCapabilityDoesNotRequestRetry(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 1
	cfg.BrokenRate = 1
	cfg.CI = &CIConfig{Runners: 1, RerunsSupported: true}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var sha string
	for i := 0; i < 100; i++ {
		if _, err := s.step(); err != nil {
			t.Fatal(err)
		}
		for key, run := range s.tests {
			if run.done {
				sha = key
			}
		}
		if sha != "" {
			break
		}
	}
	if sha == "" || s.result.CIReruns != 0 {
		t.Fatal("planner unexpectedly retried or failed to start CI")
	}
	before := slicesEvents(s.events)
	if !s.rerunFailedCI(sha) || s.rerunFailedCI(sha) {
		t.Fatal("CI rerun admission failed")
	}
	if !reflect.DeepEqual(before, s.events) || s.tests[sha].job.duration != cfg.CIDuration || s.result.CIRuns != 2 || s.ciMetrics(0).RerunsStarted != 1 {
		t.Fatal("capability changed planner events or skipped full service")
	}
	if s.tests[sha].job.id != 2 {
		t.Fatal("rerun did not get a new run identity")
	}
	e := heap.Pop(&s.scheduled).(scheduledEvent)
	s.now = e.at
	if err := s.deliver(e); err != nil {
		t.Fatal(err)
	}
	checks, err := s.forge.CheckRuns(context.Background(), s.repo, sha)
	if err != nil || len(checks) != 1 || checks[0].ID != 2 {
		t.Fatalf("completion lost rerun identity: %+v %v", checks, err)
	}

}
func slicesEvents(events []loggedEvent) []loggedEvent { return append([]loggedEvent(nil), events...) }

func TestExternalRerunAfterSuccessfulRetryHasFreshIdentity(t *testing.T) {
	cfg := smallConfig()
	cfg.Seed, cfg.PRs, cfg.FlakeRate = 6, 1, .5
	cfg.Faults.CIRerunRate = 1
	cfg.CI = &CIConfig{Runners: 1, RerunsSupported: true}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var sha string
	for i := 0; i < 100 && sha == ""; i++ {
		if _, err := s.step(); err != nil {
			t.Fatal(err)
		}
		for key, run := range s.tests {
			if run.done {
				sha = key
			}
		}
	}
	if sha == "" || s.tests[sha].success {
		t.Fatal("fixture must complete a flaky first failure")
	}
	complete := func() {
		t.Helper()
		for i, e := range s.scheduled {
			if e.kind == ciComplete && e.job == s.tests[sha].job {
				heap.Remove(&s.scheduled, i)
				s.now = e.at
				if err := s.deliver(e); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatal("missing CI completion")
	}
	check := func(id uint64, state forge.CheckState) {
		t.Helper()
		checks, err := s.forge.CheckRuns(context.Background(), s.repo, sha)
		if err != nil || len(checks) != 1 || checks[0].ID != id || checks[0].State != state {
			t.Fatalf("want run %d %s, got %+v (%v)", id, state, checks, err)
		}
	}
	if !s.rerunFailedCI(sha) {
		t.Fatal("retry rejected")
	}
	check(2, forge.Pending)
	complete()
	check(2, forge.Success)
	f := &faultForge{Forge: s.forge, sim: s, scope: "landing/" + s.state.Active.ID}
	checks, err := f.CheckRuns(context.Background(), s.repo, sha)
	if err != nil || len(checks) != 1 || checks[0].ID != 3 || checks[0].State != forge.Pending {
		t.Fatalf("external rerun reused completed retry identity: %+v (%v)", checks, err)
	}
	complete()
	check(3, forge.Success)
}

func TestCIInteractionAndLongTimeout(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 8
	cfg.Planner.BatchMaxSize = 8
	cfg.CI = &CIConfig{Runners: 50, InteractionRate: 1, CancellationSupported: true}
	_, r := runScenario(t, cfg)
	if r.Broken != 0 || r.Merged != 4 || r.Dequeued[queue.ChecksFailed] != 4 {
		t.Fatalf("interactions not isolated: %+v", r)
	}
	cfg = smallConfig()
	cfg.PRs = 5
	cfg.CIDuration = 3 * time.Hour
	cfg.Planner.CheckTimeout = cfg.CIDuration * 3 / 2
	cfg.CI = &CIConfig{Runners: 50}
	_, r = runScenario(t, cfg)
	if r.Merged != 5 || r.Dequeued[queue.ChecksTimeout] != 0 || r.CI.RunnerMinutes != 900 {
		t.Fatalf("long CI timed out: %+v", r)
	}
}

func TestCapacityAgainstSerialPlanner(t *testing.T) {
	if testing.Short() {
		t.Skip("full simulator workload: run make test-sim-exits")
	}
	for _, scenario := range []struct{ n, size int }{{500, 1}, {500, 8}, {2000, 8}} {
		n := scenario.n
		cfg := DefaultConfig()
		cfg.PRs = n
		cfg.Seed = 42
		cfg.Planner.BatchMaxSize = scenario.size
		if scenario.size > 1 {
			cfg.Planner.BatchMaxWait = 30 * time.Second
		}
		if n == 2000 {
			cfg.CIDuration = 30 * time.Minute
			cfg.Planner.BatchMaxSize = 8
			cfg.Planner.BatchMaxWait = 30 * time.Second
			cfg.Planner.CheckTimeout = time.Hour
		}
		_, planner := runScenario(t, cfg)
		c := capacity.Config{Seed: 42, Changes: n, ArrivalWindow: cfg.ArrivalWindow, CI: cfg.CIDuration, MaxWait: cfg.Planner.BatchMaxWait, CheckTimeout: cfg.Planner.CheckTimeout, BrokenRate: cfg.BrokenRate, MaxSize: max(1, cfg.Planner.BatchMaxSize), MinDepth: 1, MaxDepth: 1, Runners: 50, Cancellation: true, Target: .8}
		model, err := capacity.Run(c)
		if err != nil {
			t.Fatal(err)
		}
		stats := capacity.Summarize(model.Merges, model.Runs, model.RunnerMinutes, 0)
		t.Logf("n=%d size=%d planner: median=%s runs/merge=%.5f; model: median=%s runs/merge=%.5f", n, scenario.size, planner.MedianTimeToMerge, planner.CIRunsPerMergedPR, stats.Median, stats.RunsPerMerge)
		if stats.Merged != planner.Merged || model.Dequeued != planner.Broken || model.Runs != planner.CIRuns || stats.RunsPerMerge != planner.CIRunsPerMergedPR || stats.Median != planner.MedianTimeToMerge || stats.P95 != planner.P95TimeToMerge {
			t.Fatal("standalone model disagrees with serial simulator")
		}
	}
}

func TestZeroTailThresholdDisablesMeasurement(t *testing.T) {
	cfg := smallConfig()
	cfg.CI = &CIConfig{Runners: 1}
	_, r := runScenario(t, cfg)
	if r.Merged == 0 || r.CI.TailFraction != 0 {
		t.Fatalf("zero threshold should disable tail measurement: %+v", r.CI)
	}
}

func TestQueuedCancellationDoesNotCountAsStarted(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 0
	cfg.CI = &CIConfig{Runners: 1, CancellationSupported: true}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{"running", "queued"} {
		s.tests[sha] = &testRun{}
		s.submitCI(sha, time.Hour)
	}
	s.cancelCI("queued")
	if s.result.CIRuns != 1 || s.ciMetrics(0).CancellationsConfirmed != 1 {
		t.Fatal("queued cancellation counted an unstarted run")
	}
}
