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
	if *adopterDepth1 {
		runAdopterDepth1(t, p, 8, 30)
		return
	}
	for _, capability := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancellation-supported", false: "cancellation-unsupported"}[capability], func(t *testing.T) {
			if capability {
				t.Skip("cancellation arrives in ADR 0009 PR 4")
			}
			var samples, modelSamples []capacity.Sample
			runs, modelRuns := 0, 0
			minutes, modelMinutes := 0., 0.
			for seed := 1; seed <= 8; seed++ {
				cfg := plannerProfile(p, uint64(seed), capability, 30)
				cfg.Planner.SpeculationMaxDepth = p.Depth
				_, r := runScenario(t, cfg)
				if r.Merged+sumDequeued(r) != r.PRs {
					t.Fatal("adopter lost work")
				}
				var seedSamples, cold []capacity.Sample
				for _, m := range r.CI.Merges {
					sample := capacity.Sample{Change: m.PR, Arrival: m.Enqueued, TTM: m.TimeToMerge}
					seedSamples = append(seedSamples, sample)
					if m.Enqueued < 24*time.Hour {
						cold = append(cold, sample)
					}
				}
				samples = append(samples, seedSamples...)
				runs += r.CIRuns
				minutes += r.CI.RunnerMinutes
				day1 := capacity.Summarize(cold, 0, 0, p.Threshold)
				t.Logf("%s fixed W=4 capability=%t (cancellation unsupported in PR3) seed=%d: %+v; day1 merged=%d p50=%s p95=%s tail=%.6f", p.Name, capability, seed, capacity.Summarize(seedSamples, r.CIRuns, r.CI.RunnerMinutes, p.Threshold), day1.Merged, day1.Median, day1.P95, day1.TailFraction)
				model, err := capacity.Run(p.Config(uint64(seed), capability))
				if err != nil {
					t.Fatal(err)
				}
				modelSamples = append(modelSamples, model.Merges...)
				modelRuns += model.Runs
				modelMinutes += model.RunnerMinutes
			}
			got := capacity.Summarize(samples, runs, minutes, p.Threshold)
			predicted := capacity.Summarize(modelSamples, modelRuns, modelMinutes, p.Threshold)
			t.Logf("POOLED %s fixed W=4 capability=%t: %+v; PR1 model: %+v; delta p95=%s runs/merge=%+.6f tail=%+.6f", p.Name, capability, got, predicted, got.P95-predicted.P95, got.RunsPerMerge-predicted.RunsPerMerge, got.TailFraction-predicted.TailFraction)
			if got.P95 >= p.Threshold || got.RunsPerMerge >= 1.5 {
				t.Errorf("fixed-window adopter gate failed: p95=%s (want <%s), runs/merge=%.6f (want <1.5)", got.P95, p.Threshold, got.RunsPerMerge)
			}
		})
	}
}
func TestExitAdopterTeam(t *testing.T) { adopterExit(t, capacity.Team()) }
func TestExitAdopterSlow(t *testing.T) { adopterExit(t, capacity.Slow()) }
func TestExitAdopterAdaptive(t *testing.T) {
	t.Skip("requires ADR 0009 PR 5: adaptive window and batch size")
}

// TestAdopterDepth1Harness exercises the same configuration, runner accounting,
// pooling and real planner now, with one seed's first-day arrival slice.
func TestAdopterDepth1Harness(t *testing.T) {
	for _, p := range []capacity.Profile{capacity.Team(), capacity.Slow(), capacity.AdaptiveWorkload()} {
		t.Run(p.Name, func(t *testing.T) { runAdopterDepth1(t, p, 1, 1) })
	}
}
