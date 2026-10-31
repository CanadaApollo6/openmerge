package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LabWorkload is a named synthetic workload. Source cites where every parameter
// comes from; none is calibrated from OpenMerge dogfood or adopter data, which
// ADR 0009 seals until period B.
type LabWorkload struct {
	Name, Source string
	// Median is the typical CI duration, used by the isolation model.
	Median time.Duration
	// Calibrate asserts that the isolation model reproduces the planner's
	// bisection run count (depth one, no flakes, no base churn).
	Calibrate bool
	Config    func() Config
}

// LabStrategyCase applies one strategy on top of a workload's configuration.
type LabStrategyCase struct {
	Name, Description string
	Apply             func(*Config)
}

// LabMatrix is one workload × strategy comparison. The first strategy is the
// baseline every delta is paired against.
type LabMatrix struct {
	Name, Description string
	Workloads         []LabWorkload
	Strategies        []LabStrategyCase
	// Detail renders full per-workload tables and records workload configs in
	// the summary; sweeps render one compact row per workload.
	Detail bool
}

func benchDay() Config {
	cfg := DefaultConfig()
	cfg.Planner.BatchMaxSize = 8
	cfg.Planner.BatchMaxWait = 30 * time.Second
	cfg.CI = &CIConfig{}
	cfg.Lab = &LabConfig{}
	return cfg
}

func benchMixtureDay() Config {
	cfg := benchDay()
	cfg.Lab.Risk = RiskModel{Kind: "mixture", LowRate: .015, HighRate: .25, HighFr: .15, ScoreNoise: .5}
	cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 2 * time.Minute, Sigma: .3}
	return cfg
}

func benchBetaLoaded(prs int) Config {
	cfg := benchDay()
	cfg.PRs = prs
	cfg.Lab.Risk = RiskModel{Kind: "beta", Alpha: .4, Beta: 7.6, ScoreNoise: .5}
	cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 4 * time.Minute, Sigma: .3}
	return cfg
}

func benchDepth4(cfg Config, ci time.Duration) Config {
	cfg.CIDuration = ci
	cfg.FlakeRate = .01
	cfg.Planner.CheckTimeout = ci * 3 / 2
	cfg.Planner.BatchMaxSize = 8
	cfg.Planner.BatchMaxWait = 5 * time.Minute
	cfg.Planner.SpeculationMaxDepth = 4
	cfg.CI = &CIConfig{Runners: 50, CancellationSupported: true, RerunsSupported: true}
	return cfg
}

// benchAgentBurst is the burst depth-four workload. sigma zero keeps the fixed
// 2-minute CI; otherwise CI durations are lognormal with that sigma and the
// same median, and the check timeout rises to 3 × the median (the lognormal
// clamp), so no run times out; the cancellation sweep shows the 1.5 × case.
func benchAgentBurst(sigma float64) Config {
	cfg := benchDepth4(benchDay(), 2*time.Minute)
	cfg.PRs = 900
	cfg.Lab.Risk = RiskModel{Kind: "beta", Alpha: .5, Beta: 9.5, ScoreNoise: .5}
	cfg.Lab.Bursts = BurstModel{Fraction: .6, Size: 25, Spread: 5 * time.Minute}
	if sigma > 0 {
		cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 2 * time.Minute, Sigma: sigma}
		cfg.Planner.CheckTimeout = 3 * 2 * time.Minute
	}
	return cfg
}

func benchTeamSlow() Config {
	cfg := benchDepth4(benchDay(), 30*time.Minute)
	cfg.PRs = 600
	cfg.ArrivalWindow = 30 * 24 * time.Hour
	cfg.Lab.Risk = RiskModel{Kind: "mixture", LowRate: .005, HighRate: .155, HighFr: .1, ScoreNoise: .5}
	return cfg
}

// benchChurn adds mid-flight invalidation: rate of changes re-pushed and 2% of
// changes dequeued by request, each a uniform delay (mean delay) after the
// change first enters a started batch.
func benchChurn(cfg Config, repushRate float64, delay time.Duration) Config {
	cfg.Lab.Invalidation = InvalidationModel{RepushRate: repushRate, DequeueRate: .02, Delay: DelayModel{Kind: "uniform", Mean: delay}}
	return cfg
}

// LabWorkloads is the main benchmark matrix of workloads.
func LabWorkloads() []LabWorkload {
	return []LabWorkload{
		{
			Name: "default-day", Median: 2 * time.Minute, Calibrate: true,
			Source: "sim DefaultConfig (doc.go): 500 PRs/24h, 5% global broken, fixed 2-min CI; batch 8 / 30s wait from TestExit500PRDay",
			Config: benchDay,
		},
		{
			Name: "mixture-day", Median: 2 * time.Minute, Calibrate: true,
			Source: "default-day arrivals; synthetic risk mixture 85%@1.5% + 15%@25% (mean 5.0%, matched to DefaultConfig), logit score noise 0.5; lognormal CI median 2m sigma 0.3",
			Config: benchMixtureDay,
		},
		{
			Name: "beta-loaded", Median: 4 * time.Minute, Calibrate: true,
			Source: "synthetic: 800 PRs/24h, Beta(0.4, 7.6) risk (mean 5%), score noise 0.5, lognormal CI median 4m sigma 0.3 (about 80% of serial batch-8 capacity)",
			Config: func() Config { return benchBetaLoaded(800) },
		},
		{
			Name: "flaky-mixture-day", Median: 2 * time.Minute,
			Source: "mixture-day with 3% flakes (synthetic stress; capacity profiles use 1%)",
			Config: func() Config {
				cfg := benchMixtureDay()
				cfg.FlakeRate = .03
				return cfg
			},
		},
		{
			Name: "agent-burst-depth4", Median: 2 * time.Minute,
			Source: "capacity.AdaptiveWorkload (900/day, 2-min CI, depth 4, 5%) and Profile.Config (batch 8, 5m wait, 50 runners, 1% flakes, timeout 1.5x CI) for one day; Beta(0.5, 9.5) risk (mean 5%), noise 0.5; 60% of PRs in bursts of 25 within 5m (synthetic agent bursts); fixed CI duration",
			Config: func() Config { return benchAgentBurst(0) },
		},
		{
			Name: "agent-burst-depth4-var", Median: 2 * time.Minute,
			Source: "agent-burst-depth4 with lognormal CI (median 2m, sigma 0.6, synthetic) and timeout 3x median",
			Config: func() Config { return benchAgentBurst(.6) },
		},
		{
			Name: "agent-burst-depth4-churn", Median: 2 * time.Minute,
			Source: "agent-burst-depth4-var plus synthetic mid-flight invalidation: 10% of changes re-pushed and 2% dequeued by request, uniform 0-2m after first entering a started batch",
			Config: func() Config { return benchChurn(benchAgentBurst(.6), .1, time.Minute) },
		},
		{
			Name: "team-slow-depth4", Median: 30 * time.Minute,
			Source: "capacity.Team (20/day for 30 days, 30-min CI, depth 4, 2%) and Profile.Config; synthetic mixture 90%@0.5% + 10%@15.5% (mean 2%), noise 0.5; fixed CI duration",
			Config: benchTeamSlow,
		},
		{
			Name: "team-slow-depth4-churn", Median: 30 * time.Minute,
			Source: "team-slow-depth4 with lognormal CI (sigma 0.6, timeout 3x median) plus 10% re-pushed and 2% dequeued, uniform 0-30m after first entering a started batch (synthetic)",
			Config: func() Config {
				cfg := benchTeamSlow()
				cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 30 * time.Minute, Sigma: .6}
				cfg.Planner.CheckTimeout = 90 * time.Minute
				return benchChurn(cfg, .1, 15*time.Minute)
			},
		},
	}
}

func noCancel(c *Config) {
	ci := *c.CI
	ci.CancellationSupported = false
	c.CI = &ci
}

// LabStrategies is the main comparison. Strategy (c), Bayesian culprit
// isolation, changes the reducer's bisection rule and is therefore evaluated
// by the isolation model rather than as a planner override.
func LabStrategies() []LabStrategyCase {
	return []LabStrategyCase{
		{Name: "baseline", Description: "(a) production planner as configured by the workload", Apply: func(*Config) {}},
		{Name: "no-cancel", Description: "(a, counterfactual) the same CI without cancellation support: cancellation saving = no-cancel − baseline", Apply: noCancel},
		{Name: "tree-dedupe", Description: "(b) reuse the latest completed result for an identical tree", Apply: func(c *Config) { c.Lab.Strategy.TreeDedupe = true }},
		{Name: "global-adaptive", Description: "(d, reference) production BatchTargetPassRate 0.8 from the global failure estimate (opt-in in production)", Apply: func(c *Config) { c.Planner.BatchTargetPassRate = .8 }},
		{Name: "risk-sized", Description: "(d) per-change risk scores size each fresh batch to a 0.8 pass target", Apply: func(c *Config) { c.Lab.Strategy.RiskBatchTarget = .8 }},
		{Name: "fail-fast", Description: "(e) failing runs stop at 30% of their duration (CI fail-fast); the queue reacts and cancels earlier", Apply: func(c *Config) { c.Lab.Duration.FailFastFraction = .3 }},
		{Name: "combined", Description: "(b)+(d)+(e) together", Apply: func(c *Config) {
			c.Lab.Strategy.TreeDedupe = true
			c.Lab.Strategy.RiskBatchTarget = .8
			c.Lab.Duration.FailFastFraction = .3
		}},
	}
}

func baselineStrategy() LabStrategyCase { return LabStrategies()[0] }

// LabSuite is the full benchmark: the main matrix plus the sweeps that test
// each headline finding's sensitivity.
func LabSuite() []LabMatrix {
	var cancellation, load, noise, failFast []LabWorkload
	for _, sigma := range []float64{0, .3, .6} {
		for _, rate := range []float64{0, .1, .25} {
			cancellation = append(cancellation, LabWorkload{
				Name: fmt.Sprintf("burst σ=%.1f repush=%.0f%%", sigma, 100*rate), Median: 2 * time.Minute,
				Source: "agent-burst-depth4 (sigma 0 = fixed CI); repush rate re-pushes that share of changes, plus 2% dequeued when the rate is nonzero, uniform 0-2m after batch start",
				Config: func() Config {
					cfg := benchAgentBurst(sigma)
					if rate > 0 {
						cfg = benchChurn(cfg, rate, time.Minute)
					}
					return cfg
				},
			})
		}
	}
	cancellation = append(cancellation,
		LabWorkload{
			Name: "burst σ=0.6 repush=0% timeout 1.5×", Median: 2 * time.Minute,
			Source: "burst σ=0.6 with the fixed-CI timeout (1.5 × median), so about a quarter of runs time out",
			Config: func() Config {
				cfg := benchAgentBurst(.6)
				cfg.Planner.CheckTimeout = 3 * time.Minute
				return cfg
			},
		},
		LabWorkload{
			Name: "burst σ=0.6 repush=10% runners=3", Median: 2 * time.Minute,
			Source: "burst σ=0.6 repush=10% with 3 runners instead of 50, so runner capacity binds",
			Config: func() Config {
				cfg := benchChurn(benchAgentBurst(.6), .1, time.Minute)
				cfg.CI.Runners = 3
				return cfg
			},
		},
	)
	for _, prs := range []int{300, 450, 600, 700, 800} {
		load = append(load, LabWorkload{
			Name: fmt.Sprintf("beta-loaded %d/day", prs), Median: 4 * time.Minute, Calibrate: true,
			Source: "beta-loaded with this many PRs per day",
			Config: func() Config { return benchBetaLoaded(prs) },
		})
	}
	for _, base := range []LabWorkload{LabWorkloads()[1], LabWorkloads()[2], LabWorkloads()[4]} {
		for _, n := range []float64{0, .25, .5, 1, 2} {
			noise = append(noise, LabWorkload{
				Name: fmt.Sprintf("%s noise=%.2g", base.Name, n), Median: base.Median, Calibrate: base.Calibrate,
				Source: base.Name + " with this logit score noise (0 = perfect scores of the true probability)",
				Config: func() Config {
					cfg := base.Config()
					cfg.Lab.Risk.ScoreNoise = n
					return cfg
				},
			})
		}
	}
	for _, i := range []int{1, 2, 5} {
		failFast = append(failFast, LabWorkloads()[i])
	}
	adaptive := func(name, description string, target float64, guard bool) LabStrategyCase {
		return LabStrategyCase{Name: name, Description: description, Apply: func(c *Config) {
			c.Planner.BatchTargetPassRate = target
			c.Lab.Strategy.BacklogGuard = guard
		}}
	}
	riskSized := LabStrategies()[4]
	fastAt := func(f float64) LabStrategyCase {
		return LabStrategyCase{Name: fmt.Sprintf("fail-fast %.0f%%", 100*f), Description: fmt.Sprintf("failing runs stop at %.0f%% of their duration", 100*f),
			Apply: func(c *Config) { c.Lab.Duration.FailFastFraction = f }}
	}
	return []LabMatrix{
		{Name: "main", Description: "Main comparison: every strategy on every workload.", Workloads: LabWorkloads(), Strategies: LabStrategies(), Detail: true},
		{
			Name:        "cancellation",
			Description: "Cancellation saving as a function of CI duration variance and mid-flight invalidation rate (agent-burst-depth4).",
			Workloads:   cancellation, Strategies: []LabStrategyCase{baselineStrategy(), LabStrategies()[1]},
		},
		{
			Name:        "load-curve",
			Description: "Adaptive batch sizing against load (beta-loaded, depth one). The backlog guard keeps the configured maximum whenever a full batch is waiting.",
			Workloads:   load,
			Strategies: []LabStrategyCase{
				baselineStrategy(),
				adaptive("global-adaptive 0.8", "production BatchTargetPassRate 0.8", .8, false),
				adaptive("global-adaptive 0.6", "production BatchTargetPassRate 0.6", .6, false),
				adaptive("global-adaptive 0.8 + guard", "BatchTargetPassRate 0.8 with the sim-only backlog guard", .8, true),
				riskSized,
				{Name: "risk-sized + guard", Description: "risk-sized 0.8 with the backlog guard", Apply: func(c *Config) {
					c.Lab.Strategy.RiskBatchTarget = .8
					c.Lab.Strategy.BacklogGuard = true
				}},
			},
		},
		{
			Name:        "score-noise",
			Description: "Value of score quality: the risk score is the true probability with this much logit noise. Truth, arrivals and baseline runs are identical across noise levels.",
			Workloads:   noise, Strategies: []LabStrategyCase{baselineStrategy(), riskSized},
		},
		{
			Name:        "fail-fast",
			Description: "Fail-fast saving against the point at which a failing run stops.",
			Workloads:   failFast, Strategies: []LabStrategyCase{baselineStrategy(), fastAt(.3), fastAt(.6), fastAt(.9)},
		},
	}
}

// Stat is a mean with a two-sided 95% Student-t interval across seeds.
type Stat struct {
	N               int
	Mean, SD        float64
	Low, High, Half float64
}

func newStat(xs []float64) Stat {
	s := Stat{N: len(xs)}
	if s.N == 0 {
		return s
	}
	for _, x := range xs {
		s.Mean += x
	}
	s.Mean /= float64(s.N)
	if s.N > 1 {
		for _, x := range xs {
			s.SD += (x - s.Mean) * (x - s.Mean)
		}
		s.SD = math.Sqrt(s.SD / float64(s.N-1))
		s.Half = tQuantile975(s.N-1) * s.SD / math.Sqrt(float64(s.N))
	}
	s.Low, s.High = s.Mean-s.Half, s.Mean+s.Half
	return s
}

func tQuantile975(df int) float64 {
	table := []float64{0, 12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
		2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
		2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042}
	if df < len(table) {
		return table[df]
	}
	return 1.960
}

// LabRun is one (workload, strategy, seed) execution.
type LabRun struct {
	Seed    uint64      `json:"seed"`
	Metrics *LabMetrics `json:"metrics"`
	Error   string      `json:"error,omitempty"`
}

// LabCell aggregates one workload × strategy across seeds. Deltas are paired by
// seed against the workload's baseline, in percent.
type LabCell struct {
	Workload, Strategy string
	Runs               []LabRun
	Summary            map[string]Stat
	DeltaPercent       map[string]Stat
}

// IsolationRow summarizes one isolation policy over a workload's baseline
// failed batches. Per-batch values are averaged per seed, then across seeds.
type IsolationRow struct {
	Workload, Policy      string
	Cases                 int
	RunsPerFailedBatch    Stat
	MinutesPerFailedBatch Stat
	InnocentDelayMinutes  Stat
	DeltaRunsPercent      Stat
	DeltaInnocentDelayPct Stat
	FalseCulpritDequeues  int
	MissedCulpritLandings int
}

// Calibration compares the halving model to the planner's bisection runs.
type Calibration struct {
	Workload    string
	Seed        uint64
	Model, Real int
}

// CancellationRow is the seed-paired value of CI cancellation: the baseline
// (cancellation as the workload configures it) against no-cancel. Saved
// minutes are no-cancel minus baseline; residual minutes are what the baseline
// still charged to runs it cancelled.
type CancellationRow struct {
	Workload                 string
	Supported                bool
	SavedMinutesPerLanded    Stat
	SavingPercent            Stat
	ResidualMinutesPerLanded Stat
	P50DeltaPercent          Stat
}

// LabReport is one matrix's output.
type LabReport struct {
	Name, Description string
	Detail            bool
	Seeds             []uint64
	Workloads         []map[string]any
	Strategies        []map[string]string
	Cells             []LabCell
	Isolation         []IsolationRow
	Calibration       []Calibration
	Cancellation      []CancellationRow
}

// LabSuiteReport is the full benchmark output.
type LabSuiteReport struct {
	Seeds   []uint64
	Reports []LabReport
}

var labMetricNames = []string{
	"runner_minutes_per_landed", "runs_per_landed", "wasted_minutes_per_landed", "waste_fraction",
	"p50_latency_minutes", "p90_latency_minutes", "throughput_per_day", "dedupe_hits", "wrongful_dequeues", "landed",
	"failed_minutes_per_landed", "invalidated_minutes_per_landed", "cancelled_residual_minutes_per_landed",
	"superseded_minutes_per_landed", "rerun_minutes_per_landed", "speculative_minutes_per_landed", "bisection_minutes_per_landed",
	"dequeued",
}

// labSweepMetricNames is the subset a sweep matrix records in the summary.
var labSweepMetricNames = []string{
	"runner_minutes_per_landed", "runs_per_landed", "wasted_minutes_per_landed", "p50_latency_minutes", "p90_latency_minutes",
	"throughput_per_day", "cancelled_residual_minutes_per_landed", "landed", "dequeued",
}

func labMetricValues(m *LabMetrics) map[string]float64 {
	perLanded := func(minutes float64) float64 {
		if m.Landed == 0 {
			return 0
		}
		return minutes / float64(m.Landed)
	}
	return map[string]float64{
		"runner_minutes_per_landed": m.RunnerMinutesPerLanded, "runs_per_landed": m.RunsPerLanded,
		"wasted_minutes_per_landed": m.WastedMinutesPerLanded, "waste_fraction": m.WasteFraction,
		"p50_latency_minutes": m.P50LatencyMinutes, "p90_latency_minutes": m.P90LatencyMinutes,
		"throughput_per_day": m.ThroughputPerDay, "dedupe_hits": float64(m.DedupeHits),
		"wrongful_dequeues": float64(m.WrongfulDequeues), "landed": float64(m.Landed),
		"failed_minutes_per_landed": perLanded(m.FailedMinutes), "invalidated_minutes_per_landed": perLanded(m.InvalidatedMinutes),
		"cancelled_residual_minutes_per_landed": perLanded(m.CancelledResidualMinutes), "superseded_minutes_per_landed": perLanded(m.SupersededMinutes),
		"rerun_minutes_per_landed": perLanded(m.RerunMinutes), "speculative_minutes_per_landed": perLanded(m.SpeculativeMinutes),
		"bisection_minutes_per_landed": perLanded(m.BisectionMinutes), "dequeued": float64(m.Dequeued),
	}
}

// RunLabSuite runs every matrix in order over the same seeds.
func RunLabSuite(matrices []LabMatrix, seeds []uint64) (LabSuiteReport, error) {
	suite := LabSuiteReport{Seeds: seeds}
	for _, m := range matrices {
		report, err := RunEfficiencyLab(m.Workloads, m.Strategies, seeds)
		report.Name, report.Description, report.Detail = m.Name, m.Description, m.Detail
		suite.Reports = append(suite.Reports, report)
		if err != nil {
			return suite, fmt.Errorf("%s: %w", m.Name, err)
		}
	}
	return suite, nil
}

// RunEfficiencyLab runs every workload × strategy × seed. Runs execute in
// parallel, but each is an independent deterministic simulation, and results
// are placed by index, so the report depends only on its inputs.
func RunEfficiencyLab(workloads []LabWorkload, strategies []LabStrategyCase, seeds []uint64) (LabReport, error) {
	report := LabReport{Seeds: seeds}
	type job struct{ w, s, k int }
	results := make([][][]LabRun, len(workloads))
	var jobs []job
	for w := range workloads {
		results[w] = make([][]LabRun, len(strategies))
		for s := range strategies {
			results[w][s] = make([]LabRun, len(seeds))
			for k := range seeds {
				jobs = append(jobs, job{w, s, k})
			}
		}
	}
	queue := make(chan job)
	var wg sync.WaitGroup
	for range max(1, min(runtime.GOMAXPROCS(0), len(jobs))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				cfg := workloads[j.w].Config()
				cfg.Seed = seeds[j.k]
				strategies[j.s].Apply(&cfg)
				result, err := Run(cfg)
				run := LabRun{Seed: seeds[j.k], Metrics: result.Lab}
				if err != nil {
					run.Error = err.Error()
				}
				results[j.w][j.s][j.k] = run
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()

	var failures []string
	for w, workload := range workloads {
		cfg := workload.Config()
		cfg.Seed = seeds[0]
		report.Workloads = append(report.Workloads, map[string]any{"name": workload.Name, "source": workload.Source, "calibrated": workload.Calibrate, "config": cfg})
		for s, strategy := range strategies {
			cell := LabCell{Workload: workload.Name, Strategy: strategy.Name, Runs: results[w][s], Summary: map[string]Stat{}, DeltaPercent: map[string]Stat{}}
			for _, run := range cell.Runs {
				if run.Error != "" || run.Metrics == nil {
					failures = append(failures, fmt.Sprintf("%s/%s/seed %d: %s", workload.Name, strategy.Name, run.Seed, run.Error))
				}
			}
			for _, name := range labMetricNames {
				var xs, deltas []float64
				for k, run := range cell.Runs {
					if run.Metrics == nil {
						continue
					}
					x := labMetricValues(run.Metrics)[name]
					xs = append(xs, x)
					if base := results[w][0][k].Metrics; base != nil {
						if b := labMetricValues(base)[name]; b != 0 {
							deltas = append(deltas, 100*(x-b)/b)
						}
					}
				}
				cell.Summary[name] = newStat(xs)
				cell.DeltaPercent[name] = newStat(deltas)
			}
			report.Cells = append(report.Cells, cell)
		}
		for k, run := range results[w][0] {
			if run.Metrics != nil {
				report.Calibration = append(report.Calibration, Calibration{Workload: workload.Name, Seed: seeds[k], Model: run.Metrics.ModelHalvingBisectRuns, Real: run.Metrics.PlannerBisectionRuns})
			}
		}
		report.Isolation = append(report.Isolation, isolationRows(workload, results[w][0])...)
		if off := slices.IndexFunc(strategies, func(s LabStrategyCase) bool { return s.Name == "no-cancel" }); off > 0 {
			report.Cancellation = append(report.Cancellation, cancellationRow(workload.Name, cfg.CI.CancellationSupported, results[w][0], results[w][off]))
		}
	}
	for _, strategy := range strategies {
		report.Strategies = append(report.Strategies, map[string]string{"name": strategy.Name, "description": strategy.Description})
	}
	if len(failures) > 0 {
		return report, fmt.Errorf("lab runs failed: %s", strings.Join(failures, "; "))
	}
	return report, nil
}

func cancellationRow(workload string, supported bool, on, off []LabRun) CancellationRow {
	var saved, saving, residual, p50 []float64
	for k := range on {
		a, b := on[k].Metrics, off[k].Metrics
		if a == nil || b == nil {
			continue
		}
		saved = append(saved, b.RunnerMinutesPerLanded-a.RunnerMinutesPerLanded)
		if b.RunnerMinutesPerLanded > 0 {
			saving = append(saving, 100*(b.RunnerMinutesPerLanded-a.RunnerMinutesPerLanded)/b.RunnerMinutesPerLanded)
		}
		if a.Landed > 0 {
			residual = append(residual, a.CancelledResidualMinutes/float64(a.Landed))
		}
		if b.P50LatencyMinutes > 0 {
			p50 = append(p50, 100*(a.P50LatencyMinutes-b.P50LatencyMinutes)/b.P50LatencyMinutes)
		}
	}
	return CancellationRow{Workload: workload, Supported: supported, SavedMinutesPerLanded: newStat(saved), SavingPercent: newStat(saving),
		ResidualMinutesPerLanded: newStat(residual), P50DeltaPercent: newStat(p50)}
}

type isolationPolicyCase struct {
	name   string
	policy IsolationPolicy
	oracle bool
	fast   bool
}

var isolationPolicies = []isolationPolicyCase{
	{name: "halving (production)"},
	{name: "halving + tree dedupe", policy: IsolationPolicy{Dedupe: true}},
	{name: "culprit-first (noisy scores)", policy: IsolationPolicy{CulpritFirst: true}},
	{name: "culprit-first + tree dedupe", policy: IsolationPolicy{CulpritFirst: true, Dedupe: true}},
	{name: "culprit-first + dedupe, perfect scores", policy: IsolationPolicy{CulpritFirst: true, Dedupe: true}, oracle: true},
	{name: "halving + fail-fast 30%", fast: true},
	{name: "culprit-first + dedupe + fail-fast 30%", policy: IsolationPolicy{CulpritFirst: true, Dedupe: true}, fast: true},
}

func isolationRows(w LabWorkload, baseline []LabRun) []IsolationRow {
	type seedCost struct{ runs, minutes, delay float64 }
	costs := make([][]seedCost, len(isolationPolicies))
	rows := make([]IsolationRow, len(isolationPolicies))
	for p, pc := range isolationPolicies {
		rows[p] = IsolationRow{Workload: w.Name, Policy: pc.name}
	}
	for _, run := range baseline {
		if run.Metrics == nil || len(run.Metrics.FailedBatches) == 0 {
			continue
		}
		n := float64(len(run.Metrics.FailedBatches))
		for p, pc := range isolationPolicies {
			fail := w.Median
			if pc.fast {
				fail = time.Duration(float64(w.Median) * .3)
			}
			var total IsolationCost
			for _, c := range run.Metrics.FailedBatches {
				if pc.oracle {
					c.Scores = make([]float64, len(c.Broken))
					for i, broken := range c.Broken {
						c.Scores[i] = .001
						if broken {
							c.Scores[i] = .999
						}
					}
				}
				cost := Isolate(c, pc.policy, w.Median, fail)
				total.Runs += cost.Runs
				total.Minutes += cost.Minutes
				total.InnocentDelayMinutes += cost.InnocentDelayMinutes
				total.Innocents += cost.Innocents
				rows[p].FalseCulpritDequeues += cost.FalseCulpritDequeues
				rows[p].MissedCulpritLandings += cost.MissedCulpritLandings
			}
			rows[p].Cases += len(run.Metrics.FailedBatches)
			delay := 0.0
			if total.Innocents > 0 {
				delay = total.InnocentDelayMinutes / float64(total.Innocents)
			}
			costs[p] = append(costs[p], seedCost{float64(total.Runs) / n, total.Minutes / n, delay})
		}
	}
	for p := range rows {
		var runs, minutes, delay, deltaRuns, deltaDelay []float64
		for k, c := range costs[p] {
			runs, minutes, delay = append(runs, c.runs), append(minutes, c.minutes), append(delay, c.delay)
			if base := costs[0][k]; p > 0 {
				if base.runs > 0 {
					deltaRuns = append(deltaRuns, 100*(c.runs-base.runs)/base.runs)
				}
				if base.delay > 0 {
					deltaDelay = append(deltaDelay, 100*(c.delay-base.delay)/base.delay)
				}
			}
		}
		rows[p].RunsPerFailedBatch, rows[p].MinutesPerFailedBatch, rows[p].InnocentDelayMinutes = newStat(runs), newStat(minutes), newStat(delay)
		rows[p].DeltaRunsPercent, rows[p].DeltaInnocentDelayPct = newStat(deltaRuns), newStat(deltaDelay)
	}
	return rows
}

// JSON renders the report, including every per-seed run, deterministically.
func (r LabReport) JSON() ([]byte, error) { return json.Marshal(r) }

// JSON renders the full suite, including every per-seed run, deterministically.
func (r LabSuiteReport) JSON() ([]byte, error) { return json.Marshal(r) }

// compact is a JSON number with four significant digits.
type compact float64

func (c compact) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(c), 'g', 4, 64)), nil
}

// compactStat is [mean, 95% half-width], plus n when fewer than seeds values
// contributed (for example, a delta against a zero baseline).
func compactStat(s Stat, seeds int) []any {
	if s.N == seeds {
		return []any{compact(s.Mean), compact(s.Half)}
	}
	return []any{compact(s.Mean), compact(s.Half), s.N}
}

// SummaryJSON renders the suite without per-seed runs: every cell's mean and
// 95% half-width for each metric in the matrix's metrics list (every metric
// for the main matrix, a key subset for sweeps), plus the
// calibration and cancellation tables and, for the main and score-noise
// matrices, the isolation table. Rows of the tables are arrays in the order of
// their *_columns header. Values carry four significant digits. Workload
// configs are recorded for detailed matrices; a sweep workload's Source names
// its change from a main-matrix workload. Baseline cells omit their (zero)
// deltas.
func (r LabSuiteReport) SummaryJSON() ([]byte, error) {
	type cell struct {
		Workload string  `json:"workload"`
		Strategy string  `json:"strategy"`
		Summary  [][]any `json:"summary"`
		Delta    [][]any `json:"delta_percent,omitempty"`
	}
	type matrix struct {
		Name         string              `json:"name"`
		Description  string              `json:"description"`
		Metrics      []string            `json:"metrics"`
		Workloads    []map[string]any    `json:"workloads"`
		Strategies   []map[string]string `json:"strategies"`
		Cells        []cell              `json:"cells"`
		Isolation    [][]any             `json:"isolation,omitempty"`
		Calibration  [][]any             `json:"calibration"`
		Cancellation [][]any             `json:"cancellation,omitempty"`
	}
	out := struct {
		Seeds        []uint64 `json:"seeds"`
		Stat         string   `json:"stat"`
		Isolation    []string `json:"isolation_columns"`
		Calibration  []string `json:"calibration_columns"`
		Cancellation []string `json:"cancellation_columns"`
		Matrices     []matrix `json:"matrices"`
	}{
		Seeds: r.Seeds,
		Stat:  "[mean, 95% Student-t half-width] or [mean, half-width, n] when n differs from the seed count; delta_percent is seed-paired against the matrix's first strategy",
		Isolation: []string{"workload", "policy", "failed_batches", "runs_per_failed_batch", "minutes_per_failed_batch", "innocent_delay_minutes",
			"delta_runs_percent", "delta_innocent_delay_percent", "false_culprit_dequeues", "missed_culprit_landings"},
		Calibration:  []string{"workload", "applies", "seeds_matching", "seeds", "model_runs", "planner_runs"},
		Cancellation: []string{"workload", "supported", "saved_minutes_per_landed", "saving_percent", "residual_minutes_per_landed", "p50_delta_percent"},
	}
	n := len(r.Seeds)
	for _, report := range r.Reports {
		m := matrix{Name: report.Name, Description: report.Description, Strategies: report.Strategies, Metrics: labSweepMetricNames}
		if report.Detail {
			m.Metrics = labMetricNames
		}
		for _, w := range report.Workloads {
			entry := map[string]any{"name": w["name"], "source": w["source"], "calibrated": w["calibrated"]}
			if report.Detail {
				entry["config"] = w["config"]
			}
			m.Workloads = append(m.Workloads, entry)
		}
		baseline := ""
		if len(report.Strategies) > 0 {
			baseline = report.Strategies[0]["name"]
		}
		for _, c := range report.Cells {
			out := cell{Workload: c.Workload, Strategy: c.Strategy}
			for _, name := range m.Metrics {
				out.Summary = append(out.Summary, compactStat(c.Summary[name], n))
				if c.Strategy != baseline {
					out.Delta = append(out.Delta, compactStat(c.DeltaPercent[name], n))
				}
			}
			m.Cells = append(m.Cells, out)
		}
		if report.showsIsolation() {
			for _, row := range report.Isolation {
				m.Isolation = append(m.Isolation, []any{row.Workload, row.Policy, row.Cases, compactStat(row.RunsPerFailedBatch, n),
					compactStat(row.MinutesPerFailedBatch, n), compactStat(row.InnocentDelayMinutes, n), compactStat(row.DeltaRunsPercent, n),
					compactStat(row.DeltaInnocentDelayPct, n), row.FalseCulpritDequeues, row.MissedCulpritLandings})
			}
		}
		for _, a := range report.calibrationSummary() {
			m.Calibration = append(m.Calibration, []any{a.workload, a.applies, a.match, a.n, a.model, a.real})
		}
		for _, row := range report.Cancellation {
			m.Cancellation = append(m.Cancellation, []any{row.Workload, row.Supported, compactStat(row.SavedMinutesPerLanded, n),
				compactStat(row.SavingPercent, n), compactStat(row.ResidualMinutesPerLanded, n), compactStat(row.P50DeltaPercent, n)})
		}
		out.Matrices = append(out.Matrices, m)
	}
	return json.Marshal(out)
}

type calibrationAggregate struct {
	workload              string
	applies               bool
	match, n, model, real int
}

func (r LabReport) calibrationSummary() []calibrationAggregate {
	applies := map[string]bool{}
	for _, w := range r.Workloads {
		applies[w["name"].(string)], _ = w["calibrated"].(bool)
	}
	var out []calibrationAggregate
	index := map[string]int{}
	for _, c := range r.Calibration {
		i, ok := index[c.Workload]
		if !ok {
			i = len(out)
			index[c.Workload] = i
			out = append(out, calibrationAggregate{workload: c.Workload, applies: applies[c.Workload]})
		}
		a := &out[i]
		a.n++
		a.model += c.Model
		a.real += c.Real
		if c.Model == c.Real {
			a.match++
		}
	}
	return out
}

func fmtStat(s Stat, digits int) string {
	if s.N == 0 {
		return "–"
	}
	return fmt.Sprintf("%.*f ±%.*f", digits, s.Mean, digits, s.Half)
}

func fmtDelta(s Stat) string {
	if s.N == 0 {
		return "–"
	}
	return fmt.Sprintf("%+.1f%% ±%.1f", s.Mean, s.Half)
}

// Markdown renders every matrix.
func (r LabSuiteReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Seeds: %d (%v). Values are means with 95%% Student-t half-widths across seeds; Δ is the seed-paired change versus each matrix's baseline.\n\n", len(r.Seeds), r.Seeds)
	for _, report := range r.Reports {
		fmt.Fprintf(&b, "## Matrix: %s\n\n%s\n\n", report.Name, report.Description)
		b.WriteString(report.Markdown())
	}
	return b.String()
}

// Markdown renders one matrix: full tables when detailed, else one compact row
// per workload.
func (r LabReport) Markdown() string {
	var b strings.Builder
	if r.Detail {
		r.detailMarkdown(&b)
	} else {
		r.sweepMarkdown(&b)
	}
	if len(r.Cancellation) > 0 {
		b.WriteString("#### Cancellation saving (no-cancel − baseline, seed-paired)\n\nResidual is the time the baseline still charged to runs it cancelled; it is waste, not saving.\n\n")
		b.WriteString("| workload | cancellation | saved runner-min/landed | saving % | residual cancelled min/landed | Δ p50 vs no-cancel |\n|---|---|---|---|---|---|\n")
		for _, row := range r.Cancellation {
			if !row.Supported {
				fmt.Fprintf(&b, "| %s | not supported | – | – | – | – |\n", row.Workload)
				continue
			}
			fmt.Fprintf(&b, "| %s | supported | %s | %s | %s | %s |\n", row.Workload, fmtStat(row.SavedMinutesPerLanded, 3), fmtDelta(row.SavingPercent),
				fmtStat(row.ResidualMinutesPerLanded, 3), fmtDelta(row.P50DeltaPercent))
		}
		b.WriteString("\n")
	}
	switch {
	case r.Detail:
		r.isolationMarkdown(&b)
	case r.showsIsolation():
		r.isolationPivotMarkdown(&b)
	}
	return b.String()
}

// showsIsolation reports whether the isolation model is part of this matrix's
// findings: the main matrix and the score-noise sweep.
func (r LabReport) showsIsolation() bool { return r.Detail || r.Name == "score-noise" }

func (r LabReport) detailMarkdown(b *strings.Builder) {
	for _, w := range r.Workloads {
		fmt.Fprintf(b, "### %s\n\n%s\n\n", w["name"], w["source"])
		b.WriteString("| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Cells {
			if c.Workload != w["name"] {
				continue
			}
			s, dp := c.Summary, c.DeltaPercent
			waste := s["waste_fraction"]
			waste.Mean, waste.Half = waste.Mean*100, waste.Half*100
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Strategy,
				fmtStat(s["runner_minutes_per_landed"], 2), fmtDelta(dp["runner_minutes_per_landed"]), fmtStat(s["runs_per_landed"], 3),
				fmtStat(s["wasted_minutes_per_landed"], 2), fmtStat(waste, 1), fmtStat(s["p50_latency_minutes"], 1), fmtDelta(dp["p50_latency_minutes"]),
				fmtStat(s["p90_latency_minutes"], 1), fmtDelta(dp["p90_latency_minutes"]), fmtStat(s["throughput_per_day"], 0), fmtStat(s["dedupe_hits"], 1), fmtStat(s["wrongful_dequeues"], 1))
		}
		b.WriteString("\nRunner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs. Cancelled is residual waste charged before cancellation.\n\n")
		b.WriteString("| strategy | failed | invalidated | cancelled (residual) | superseded | same-SHA reruns | on speculative predecessor | bisection |\n|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Cells {
			if c.Workload != w["name"] {
				continue
			}
			s := c.Summary
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Strategy, fmtStat(s["failed_minutes_per_landed"], 2), fmtStat(s["invalidated_minutes_per_landed"], 2),
				fmtStat(s["cancelled_residual_minutes_per_landed"], 3), fmtStat(s["superseded_minutes_per_landed"], 2), fmtStat(s["rerun_minutes_per_landed"], 2),
				fmtStat(s["speculative_minutes_per_landed"], 2), fmtStat(s["bisection_minutes_per_landed"], 2))
		}
		b.WriteString("\n")
	}
}

func (r LabReport) sweepMarkdown(b *strings.Builder) {
	b.WriteString("| workload | baseline runner-min/landed | baseline p50 (min) | baseline landed/day |")
	sep := "|---|---|---|---|"
	for _, s := range r.Strategies[1:] {
		fmt.Fprintf(b, " %s Δ min | %s Δ p50 |", s["name"], s["name"])
		sep += "---|---|"
	}
	b.WriteString("\n" + sep + "\n")
	for _, w := range r.Workloads {
		var row []string
		for _, c := range r.Cells {
			if c.Workload != w["name"] {
				continue
			}
			if len(row) == 0 {
				row = append(row, fmtStat(c.Summary["runner_minutes_per_landed"], 2), fmtStat(c.Summary["p50_latency_minutes"], 1), fmtStat(c.Summary["throughput_per_day"], 0))
				continue
			}
			row = append(row, fmtDelta(c.DeltaPercent["runner_minutes_per_landed"]), fmtDelta(c.DeltaPercent["p50_latency_minutes"]))
		}
		fmt.Fprintf(b, "| %s | %s |\n", w["name"], strings.Join(row, " | "))
	}
	b.WriteString("\n")
}

// isolationPivotMarkdown renders one row per workload: Δ CI runs per failed
// batch versus production halving for each alternative policy.
func (r LabReport) isolationPivotMarkdown(b *strings.Builder) {
	b.WriteString("#### Culprit isolation model: Δ runs per failed batch versus production halving\n\nWorkloads marked uncalibrated are depth four, where the halving model does not reproduce the planner.\n\n| workload | calibrated | failed batches |")
	sep := "|---|---|---|"
	for _, p := range isolationPolicies[1:] {
		fmt.Fprintf(b, " %s |", p.name)
		sep += "---|"
	}
	b.WriteString("\n" + sep + "\n")
	calibrated := map[string]bool{}
	for _, w := range r.Workloads {
		calibrated[w["name"].(string)], _ = w["calibrated"].(bool)
	}
	for i := 0; i+len(isolationPolicies) <= len(r.Isolation); i += len(isolationPolicies) {
		rows := r.Isolation[i : i+len(isolationPolicies)]
		fmt.Fprintf(b, "| %s | %s | %d |", rows[0].Workload, map[bool]string{true: "yes", false: "**no**"}[calibrated[rows[0].Workload]], rows[0].Cases)
		for _, row := range rows[1:] {
			fmt.Fprintf(b, " %s |", fmtDelta(row.DeltaRunsPercent))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func (r LabReport) isolationMarkdown(b *strings.Builder) {
	b.WriteString("#### Culprit isolation model (strategy c)\n\n")
	b.WriteString("Replays every failed fresh batch from the baseline runs (deterministic CI, no flakes). Runs and minutes exclude the batch's own failing run; innocent delay is the mean time from the batch failure until an innocent change's landing run completes, serial at depth one. Rows for workloads where calibration does not apply (depth four) are uncalibrated.\n\n")
	b.WriteString("| workload | calibrated | policy | failed batches | runs/failed batch | Δ runs | runner-min/failed batch | innocent delay (min) | Δ delay | false culprits |\n|---|---|---|---|---|---|---|---|---|---|\n")
	calibrated := map[string]bool{}
	for _, w := range r.Workloads {
		calibrated[w["name"].(string)], _ = w["calibrated"].(bool)
	}
	for _, row := range r.Isolation {
		fmt.Fprintf(b, "| %s | %s | %s | %d | %s | %s | %s | %s | %s | %d |\n", row.Workload, map[bool]string{true: "yes", false: "**no**"}[calibrated[row.Workload]], row.Policy, row.Cases,
			fmtStat(row.RunsPerFailedBatch, 2), fmtDelta(row.DeltaRunsPercent), fmtStat(row.MinutesPerFailedBatch, 1), fmtStat(row.InnocentDelayMinutes, 1), fmtDelta(row.DeltaInnocentDelayPct), row.FalseCulpritDequeues)
	}
	b.WriteString("\n#### Isolation model calibration (halving model vs planner bisection runs, baseline)\n\nThe model claims exactness only at depth one; at depth four, bisection interacts with failed-check retries and chain invalidation.\n\n| workload | applies | seeds matching | model runs | planner runs |\n|---|---|---|---|---|\n")
	for _, a := range r.calibrationSummary() {
		fmt.Fprintf(b, "| %s | %s | %d/%d | %d | %d |\n", a.workload, map[bool]string{true: "yes", false: "n/a"}[a.applies], a.match, a.n, a.model, a.real)
	}
	b.WriteString("\n")
}
