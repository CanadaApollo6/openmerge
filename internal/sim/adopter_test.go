package sim

import (
	"flag"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/sim/capacity"
)

var adopterDepth1 = flag.Bool("sim-adopter-depth1", false, "run full 8x30-day adopter workloads against today's depth-1 planner (no speculative exit assertions)")

func plannerProfile(p capacity.Profile, seed uint64, cancel bool, days int) Config {
	cfg := DefaultConfig()
	cfg.Seed = seed
	cfg.PRs = p.PerDay * days
	cfg.ArrivalWindow = time.Duration(days) * 24 * time.Hour
	cfg.BrokenRate = p.BrokenRate
	cfg.FlakeRate = .01
	cfg.CIDuration = p.CI
	cfg.Planner.BatchMaxSize = 8
	cfg.Planner.BatchMaxWait = 5 * time.Minute
	cfg.Planner.CheckTimeout = p.CI * 3 / 2
	cfg.CI = &CIConfig{Runners: 50, CancellationSupported: cancel, RerunsSupported: true, TailThreshold: p.Threshold}
	return cfg
}
func runAdopterDepth1(t *testing.T, p capacity.Profile, seeds, days int) {
	t.Helper()
	for _, cancel := range []bool{true, false} {
		var samples []capacity.Sample
		runs := 0
		minutes := 0.
		for seed := 1; seed <= seeds; seed++ {
			cfg := plannerProfile(p, uint64(seed), cancel, days)
			_, r := runScenario(t, cfg)
			if r.CI.RunsStarted != r.CIRuns || r.Merged == 0 || r.Merged+sumDequeued(r) != r.PRs {
				t.Fatalf("depth-1 harness lost work: %+v", r)
			}
			for _, sample := range r.CI.Merges {
				samples = append(samples, capacity.Sample{Change: sample.PR, Arrival: sample.Enqueued, TTM: sample.TimeToMerge})
			}
			runs += r.CIRuns
			minutes += r.CI.RunnerMinutes
		}
		t.Logf("%s depth 1 (adaptive/retry capabilities await planner support), cancellation=%t: %+v", p.Name, cancel, capacity.Summarize(samples, runs, minutes, p.Threshold))
	}
}
func sumDequeued(r Result) int {
	n := 0
	for _, count := range r.Dequeued {
		n += count
	}
	return n
}
func adopterExit(t *testing.T, p capacity.Profile) {
	t.Helper()
	if !*adopterDepth1 {
		t.Skip("requires ADR 0009 PR 3: speculative chain")
	}
	// Deliberately no adopter gate assertions on the serial fallback. PR 3 must
	// connect p.Depth, retry decisions and adaptive size to the real planner and
	// assert pooled latency/cost gates before removing this dependency skip.
	runAdopterDepth1(t, p, 8, 30)
}
func TestExitAdopterTeam(t *testing.T)     { adopterExit(t, capacity.Team()) }
func TestExitAdopterSlow(t *testing.T)     { adopterExit(t, capacity.Slow()) }
func TestExitAdopterAdaptive(t *testing.T) { adopterExit(t, capacity.AdaptiveWorkload()) }

// TestAdopterDepth1Harness exercises the same configuration, runner accounting,
// pooling and real planner now, with one seed's first-day arrival slice.
func TestAdopterDepth1Harness(t *testing.T) {
	for _, p := range []capacity.Profile{capacity.Team(), capacity.Slow(), capacity.AdaptiveWorkload()} {
		t.Run(p.Name, func(t *testing.T) { runAdopterDepth1(t, p, 1, 1) })
	}
}
