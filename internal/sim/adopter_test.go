package sim

import (
	"encoding/json"
	"flag"
	"strconv"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/sim/capacity"
)

var adaptiveFullExit = flag.Bool("sim-adaptive-full-exit", false, "run the pinned 8x30-day adaptive sizing exit and paired fixed-size controls (slow)")

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
			t.Parallel()
			var samples, modelSamples []capacity.Sample
			runs, modelRuns, wasted, requested, confirmed, unsupported := 0, 0, 0, 0, 0, 0
			minutes, modelMinutes := 0., 0.
			for seed := 1; seed <= 8; seed++ {
				cfg := plannerProfile(p, uint64(seed), capability, 30)
				cfg.Planner.SpeculationMaxDepth = p.Depth
				s, r := runScenario(t, cfg)
				reportProfile(t, p, "fixed-8", capability, seed, s, r)
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
				wasted += r.CI.WastedRuns
				requested += r.CI.CancellationsRequested
				confirmed += r.CI.CancellationsConfirmed
				unsupported += r.CI.CancellationsUnsupported
				day1 := capacity.Summarize(cold, 0, 0, p.Threshold)
				t.Logf("%s AIMD W=1..4 capability=%t seed=%d: %+v; day1 merged=%d p50=%s p95=%s tail=%.6f", p.Name, capability, seed, capacity.Summarize(seedSamples, r.CIRuns, r.CI.RunnerMinutes, p.Threshold), day1.Merged, day1.Median, day1.P95, day1.TailFraction)
				t.Logf("seed=%d wasted runs=%d; cancellation requested=%d confirmed=%d unsupported=%d", seed, r.CI.WastedRuns, r.CI.CancellationsRequested, r.CI.CancellationsConfirmed, r.CI.CancellationsUnsupported)
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
			t.Logf("POOLED %s AIMD W=1..4 capability=%t: %+v; PR1 model: %+v; delta p95=%s runs/merge=%+.6f tail=%+.6f", p.Name, capability, got, predicted, got.P95-predicted.P95, got.RunsPerMerge-predicted.RunsPerMerge, got.TailFraction-predicted.TailFraction)
			t.Logf("POOLED %s cancellation=%t wasted runs=%d; cancellation requested=%d confirmed=%d unsupported=%d", p.Name, capability, wasted, requested, confirmed, unsupported)
			if requested == 0 || wasted == 0 || (capability && (confirmed != requested || unsupported != 0)) || (!capability && (unsupported != requested || confirmed != 0)) {
				t.Errorf("adopter did not exercise the selected cancellation capability: requested=%d confirmed=%d unsupported=%d wasted=%d", requested, confirmed, unsupported, wasted)
			}
			if got.P95 >= p.Threshold || got.RunsPerMerge >= 1.5 {
				t.Errorf("adaptive-window adopter gate failed: p95=%s (want <%s), runs/merge=%.6f (want <1.5)", got.P95, p.Threshold, got.RunsPerMerge)
			}
		})
	}
}
func TestExitAdopterTeam(t *testing.T) {
	if testing.Short() {
		t.Skip("full simulator workload: run make test-sim-exits")
	}
	adopterExit(t, capacity.Team())
}
func TestExitAdopterSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("full simulator workload: run make test-sim-exits")
	}
	adopterExit(t, capacity.Slow())
}
func TestExitAdopterAdaptive(t *testing.T) {
	if testing.Short() || !*adaptiveFullExit {
		t.Skip("full planner-backed 8x30-day exit: enable -sim-adaptive-full-exit; the first-day harness runs by default")
	}
	adaptiveExit(t, 8, 30)
}

// TestAdopterDepth1Harness exercises the same configuration, runner accounting,
// pooling and real planner now, with one seed's first-day arrival slice.
func TestAdopterDepth1Harness(t *testing.T) {
	for _, p := range []capacity.Profile{capacity.Team(), capacity.Slow(), capacity.AdaptiveWorkload()} {
		t.Run(p.Name, func(t *testing.T) { runAdopterDepth1(t, p, 1, 1) })
	}
}

// profileRecord keeps the reproducible report machine-readable without making
// latency percentiles an average of seed percentiles.
type profileRecord struct {
	Profile, Policy                                                                 string
	Cancellation                                                                    bool
	Seed, Merged, Runs, Wasted, CancelRequested, CancelConfirmed, CancelUnsupported int
	MedianMinutes, P95Minutes, RunsPerMerge, RunnerMinutesPerMerge, TailFraction    float64
	MeanWindow, SteadyWindow, ColdWindow                                            float64
	FinalWindow                                                                     int
	ColdMerged                                                                      int
	ColdMedianMinutes, ColdP95Minutes, ColdRunsPerMerge, ColdRunnerMinutesPerMerge  float64
}

func reportProfile(t *testing.T, p capacity.Profile, policy string, cancel bool, seed int, s *simulation, r Result) {
	t.Helper()
	var cold []capacity.Sample
	for _, m := range r.CI.Merges {
		if m.Enqueued < 24*time.Hour {
			cold = append(cold, capacity.Sample{Change: m.PR, Arrival: m.Enqueued, TTM: m.TimeToMerge})
		}
	}
	var coldRuns, coldMinutes float64
	for _, job := range s.ciJobs {
		if job.started.IsZero() {
			continue
		}
		run := s.tests[job.sha]
		n := 0
		for _, pr := range run.heads {
			if s.prs[pr.PR].firstEnqueued.Sub(s.start) < 24*time.Hour {
				n++
			}
		}
		fraction := float64(n) / float64(len(run.heads))
		coldRuns += fraction
		coldMinutes += fraction * job.finished.Sub(job.started).Minutes()
	}
	c := capacity.Summarize(cold, 0, 0, p.Threshold)
	row := profileRecord{Profile: p.Name, Policy: policy, Cancellation: cancel, Seed: seed, Merged: r.Merged, Runs: r.CIRuns, Wasted: r.CI.WastedRuns, CancelRequested: r.CI.CancellationsRequested, CancelConfirmed: r.CI.CancellationsConfirmed, CancelUnsupported: r.CI.CancellationsUnsupported,
		MedianMinutes: r.MedianTimeToMerge.Minutes(), P95Minutes: r.P95TimeToMerge.Minutes(), RunsPerMerge: r.CIRunsPerMergedPR, RunnerMinutesPerMerge: r.CI.RunnerMinutesPerMergedChange, TailFraction: r.CI.TailFraction,
		MeanWindow: r.CI.MeanWindow, SteadyWindow: r.CI.SteadyMeanWindow, ColdWindow: r.CI.Day1MeanWindow, FinalWindow: r.CI.FinalWindow, ColdMerged: c.Merged, ColdMedianMinutes: c.Median.Minutes(), ColdP95Minutes: c.P95.Minutes()}
	if c.Merged > 0 {
		row.ColdRunsPerMerge = coldRuns / float64(c.Merged)
		row.ColdRunnerMinutesPerMerge = coldMinutes / float64(c.Merged)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PROFILE %s", raw)
}
func adaptiveExit(t *testing.T, seeds, days int) {
	t.Helper()
	p := capacity.AdaptiveWorkload()
	for _, cancel := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancellation-supported", false: "cancellation-unsupported"}[cancel], func(t *testing.T) {
			var allAdaptive, allFixed []capacity.Sample
			runs, baselineRuns := 0, 0
			minutes, baselineMinutes := 0., 0.
			pairs := make([][2]Result, seeds)
			// Each seed owns its forge/log/RNG. Bound resident simulations while
			// letting the expensive full profile use independent CPU cores.
			slots := make(chan struct{}, 3)
			t.Run("seeds", func(t *testing.T) {
				for seed := 1; seed <= seeds; seed++ {
					t.Run(strconv.Itoa(seed), func(t *testing.T) {
						t.Parallel()
						slots <- struct{}{}
						defer func() { <-slots }()

						cfg := plannerProfile(p, uint64(seed), cancel, days)
						cfg.Planner.SpeculationMaxDepth = p.Depth
						fixedSim, fixed := runScenario(t, cfg)
						reportProfile(t, p, "fixed-8", cancel, seed, fixedSim, fixed)
						cfg.Planner.BatchTargetPassRate = .8
						adaptiveSim, adaptive := runScenario(t, cfg)
						reportProfile(t, p, "adaptive", cancel, seed, adaptiveSim, adaptive)
						if adaptive.Merged+sumDequeued(adaptive) != adaptive.PRs || fixed.Merged+sumDequeued(fixed) != fixed.PRs {
							t.Fatal("adaptive profile lost work")
						}
						if adaptive.CIRunsPerMergedPR >= 1.5 || adaptive.MedianTimeToMerge > fixed.MedianTimeToMerge {
							t.Errorf("adaptive seed %d failed: runs/merge=%f median=%s fixed=%s", seed, adaptive.CIRunsPerMergedPR, adaptive.MedianTimeToMerge, fixed.MedianTimeToMerge)
						}
						// Additional regression gate from the PR 1 review; it strengthens the ADR
						// check without changing the pinned workload or its original thresholds.
						if adaptive.P95TimeToMerge > fixed.P95TimeToMerge {
							t.Errorf("adaptive seed %d p95=%s > fixed=%s", seed, adaptive.P95TimeToMerge, fixed.P95TimeToMerge)
						}
						pairs[seed-1] = [2]Result{adaptive, fixed}
					})
				}
			})
			if t.Failed() {
				return
			}
			for _, pair := range pairs {
				adaptive, fixed := pair[0], pair[1]
				for _, m := range adaptive.CI.Merges {
					allAdaptive = append(allAdaptive, capacity.Sample{Change: m.PR, Arrival: m.Enqueued, TTM: m.TimeToMerge})
				}
				for _, m := range fixed.CI.Merges {
					allFixed = append(allFixed, capacity.Sample{Change: m.PR, Arrival: m.Enqueued, TTM: m.TimeToMerge})
				}
				runs += adaptive.CIRuns
				baselineRuns += fixed.CIRuns
				minutes += adaptive.CI.RunnerMinutes
				baselineMinutes += fixed.CI.RunnerMinutes
			}
			a := capacity.Summarize(allAdaptive, runs, minutes, p.Threshold)
			f := capacity.Summarize(allFixed, baselineRuns, baselineMinutes, p.Threshold)
			t.Logf("POOLED Adaptive cancellation=%t: %+v; fixed-8: %+v", cancel, a, f)
			if a.RunsPerMerge >= 1.5 || a.Median > f.Median || a.P95 > f.P95 {
				t.Errorf("pooled adaptive gate failed")
			}
		})
	}
}
func TestAdaptiveSizingPlannerHarness(t *testing.T) { adaptiveExit(t, 1, 1) }
