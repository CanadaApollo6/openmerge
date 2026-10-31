package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"runtime"
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

// LabWorkloads is the default benchmark matrix of workloads.
func LabWorkloads() []LabWorkload {
	day := func() Config {
		cfg := DefaultConfig()
		cfg.Planner.BatchMaxSize = 8
		cfg.Planner.BatchMaxWait = 30 * time.Second
		cfg.CI = &CIConfig{}
		cfg.Lab = &LabConfig{}
		return cfg
	}
	mixture := func() Config {
		cfg := day()
		cfg.Lab.Risk = RiskModel{Kind: "mixture", LowRate: .015, HighRate: .25, HighFr: .15, ScoreNoise: .5}
		cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 2 * time.Minute, Sigma: .3}
		return cfg
	}
	depth4 := func(cfg Config, ci time.Duration) Config {
		cfg.CIDuration = ci
		cfg.FlakeRate = .01
		cfg.Planner.CheckTimeout = ci * 3 / 2
		cfg.Planner.BatchMaxSize = 8
		cfg.Planner.BatchMaxWait = 5 * time.Minute
		cfg.Planner.SpeculationMaxDepth = 4
		cfg.CI = &CIConfig{Runners: 50, CancellationSupported: true, RerunsSupported: true}
		return cfg
	}
	return []LabWorkload{
		{
			Name: "default-day", Median: 2 * time.Minute, Calibrate: true,
			Source: "sim DefaultConfig (doc.go): 500 PRs/24h, 5% global broken, fixed 2-min CI; batch 8 / 30s wait from TestExit500PRDay",
			Config: day,
		},
		{
			Name: "mixture-day", Median: 2 * time.Minute, Calibrate: true,
			Source: "default-day arrivals; synthetic risk mixture 85%@1.5% + 15%@25% (mean 5.0%, matched to DefaultConfig), logit score noise 0.5; lognormal CI median 2m sigma 0.3",
			Config: mixture,
		},
		{
			Name: "beta-loaded", Median: 4 * time.Minute, Calibrate: true,
			Source: "synthetic: 800 PRs/24h, Beta(0.4, 7.6) risk (mean 5%), score noise 0.5, lognormal CI median 4m sigma 0.3 (about 80% of serial batch-8 capacity)",
			Config: func() Config {
				cfg := day()
				cfg.PRs = 800
				cfg.Lab.Risk = RiskModel{Kind: "beta", Alpha: .4, Beta: 7.6, ScoreNoise: .5}
				cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 4 * time.Minute, Sigma: .3}
				return cfg
			},
		},
		{
			Name: "flaky-mixture-day", Median: 2 * time.Minute,
			Source: "mixture-day with 3% flakes (synthetic stress; capacity profiles use 1%)",
			Config: func() Config {
				cfg := mixture()
				cfg.FlakeRate = .03
				return cfg
			},
		},
		{
			Name: "agent-burst-depth4", Median: 2 * time.Minute,
			Source: "capacity.AdaptiveWorkload (900/day, 2-min CI, depth 4, 5%) and Profile.Config (batch 8, 5m wait, 50 runners, 1% flakes, timeout 1.5x CI) for one day; Beta(0.5, 9.5) risk (mean 5%), noise 0.5; 60% of PRs in bursts of 25 within 5m (synthetic agent bursts)",
			Config: func() Config {
				cfg := depth4(day(), 2*time.Minute)
				cfg.PRs = 900
				cfg.Lab.Risk = RiskModel{Kind: "beta", Alpha: .5, Beta: 9.5, ScoreNoise: .5}
				cfg.Lab.Bursts = BurstModel{Fraction: .6, Size: 25, Spread: 5 * time.Minute}
				return cfg
			},
		},
		{
			Name: "team-slow-depth4", Median: 30 * time.Minute,
			Source: "capacity.Team (20/day for 30 days, 30-min CI, depth 4, 2%) and Profile.Config; synthetic mixture 90%@0.5% + 10%@15.5% (mean 2%), noise 0.5",
			Config: func() Config {
				cfg := depth4(day(), 30*time.Minute)
				cfg.PRs = 600
				cfg.ArrivalWindow = 30 * 24 * time.Hour
				cfg.Lab.Risk = RiskModel{Kind: "mixture", LowRate: .005, HighRate: .155, HighFr: .1, ScoreNoise: .5}
				return cfg
			},
		},
	}
}

// LabStrategies is the first comparison. Strategy (c), Bayesian culprit
// isolation, changes the reducer's bisection rule and is therefore evaluated
// by the isolation model rather than as a planner override.
func LabStrategies() []LabStrategyCase {
	return []LabStrategyCase{
		{Name: "baseline", Description: "(a) production planner as configured by the workload", Apply: func(*Config) {}},
		{Name: "tree-dedupe", Description: "(b) reuse the latest completed result for an identical tree", Apply: func(c *Config) { c.Lab.Strategy.TreeDedupe = true }},
		{Name: "global-adaptive", Description: "(d, reference) production BatchTargetPassRate 0.8 from the global failure estimate", Apply: func(c *Config) { c.Planner.BatchTargetPassRate = .8 }},
		{Name: "risk-sized", Description: "(d) per-change risk scores size each fresh batch to a 0.8 pass target", Apply: func(c *Config) { c.Lab.Strategy.RiskBatchTarget = .8 }},
		{Name: "fail-fast", Description: "(e) failing runs stop at 30% of their duration (CI fail-fast); the queue reacts and cancels earlier", Apply: func(c *Config) { c.Lab.Duration.FailFastFraction = .3 }},
		{Name: "combined", Description: "(b)+(d)+(e) together", Apply: func(c *Config) {
			c.Lab.Strategy.TreeDedupe = true
			c.Lab.Strategy.RiskBatchTarget = .8
			c.Lab.Duration.FailFastFraction = .3
		}},
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

// LabReport is the harness output.
type LabReport struct {
	Seeds       []uint64
	Workloads   []map[string]any
	Strategies  []map[string]string
	Cells       []LabCell
	Isolation   []IsolationRow
	Calibration []Calibration
}

var labMetricNames = []string{
	"runner_minutes_per_landed", "runs_per_landed", "wasted_minutes_per_landed", "waste_fraction",
	"p50_latency_minutes", "p90_latency_minutes", "throughput_per_day", "dedupe_hits", "wrongful_dequeues", "landed",
	"failed_minutes_per_landed", "invalidated_minutes_per_landed", "cancelled_minutes_per_landed",
	"superseded_minutes_per_landed", "rerun_minutes_per_landed", "speculative_minutes_per_landed", "bisection_minutes_per_landed",
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
		"cancelled_minutes_per_landed": perLanded(m.CancelledMinutes), "superseded_minutes_per_landed": perLanded(m.SupersededMinutes),
		"rerun_minutes_per_landed": perLanded(m.RerunMinutes), "speculative_minutes_per_landed": perLanded(m.SpeculativeMinutes),
		"bisection_minutes_per_landed": perLanded(m.BisectionMinutes),
	}
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
	}
	for _, strategy := range strategies {
		report.Strategies = append(report.Strategies, map[string]string{"name": strategy.Name, "description": strategy.Description})
	}
	if len(failures) > 0 {
		return report, fmt.Errorf("lab runs failed: %s", strings.Join(failures, "; "))
	}
	return report, nil
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

// JSON renders the report deterministically.
func (r LabReport) JSON() ([]byte, error) { return json.Marshal(r) }

// Markdown renders the comparison tables.
func (r LabReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Seeds: %d (%v). Values are means with 95%% Student-t half-widths across seeds; Δ is the seed-paired change versus baseline.\n\n", len(r.Seeds), r.Seeds)
	f := func(s Stat, digits int) string {
		if s.N == 0 {
			return "–"
		}
		return fmt.Sprintf("%.*f ±%.*f", digits, s.Mean, digits, s.Half)
	}
	d := func(s Stat) string {
		if s.N == 0 {
			return "–"
		}
		return fmt.Sprintf("%+.1f%% ±%.1f", s.Mean, s.Half)
	}
	for _, w := range r.Workloads {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", w["name"], w["source"])
		b.WriteString("| strategy | runner-min/landed | Δ | runs/landed | wasted-min/landed | waste % | p50 latency (min) | Δ p50 | p90 latency (min) | Δ p90 | landed/day | dedupe hits | wrongful dequeues |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Cells {
			if c.Workload != w["name"] {
				continue
			}
			s, dp := c.Summary, c.DeltaPercent
			waste := s["waste_fraction"]
			waste.Mean, waste.Half = waste.Mean*100, waste.Half*100
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Strategy,
				f(s["runner_minutes_per_landed"], 2), d(dp["runner_minutes_per_landed"]), f(s["runs_per_landed"], 3),
				f(s["wasted_minutes_per_landed"], 2), f(waste, 1), f(s["p50_latency_minutes"], 1), d(dp["p50_latency_minutes"]),
				f(s["p90_latency_minutes"], 1), d(dp["p90_latency_minutes"]), f(s["throughput_per_day"], 0), f(s["dedupe_hits"], 1), f(s["wrongful_dequeues"], 1))
		}
		b.WriteString("\nRunner-minutes per landed change by cause. Waste causes are disjoint; rerun, speculative and bisection minutes are overlapping views of all runs.\n\n")
		b.WriteString("| strategy | failed | invalidated | cancelled | superseded | same-SHA reruns | on speculative predecessor | bisection |\n|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Cells {
			if c.Workload != w["name"] {
				continue
			}
			s := c.Summary
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", c.Strategy, f(s["failed_minutes_per_landed"], 2), f(s["invalidated_minutes_per_landed"], 2),
				f(s["cancelled_minutes_per_landed"], 3), f(s["superseded_minutes_per_landed"], 2), f(s["rerun_minutes_per_landed"], 2),
				f(s["speculative_minutes_per_landed"], 2), f(s["bisection_minutes_per_landed"], 2))
		}
		b.WriteString("\n")
	}
	b.WriteString("### Culprit isolation model (strategy c)\n\n")
	b.WriteString("Replays every failed fresh batch from the baseline runs (deterministic CI, no flakes). Runs and minutes exclude the batch's own failing run; innocent delay is the mean time from the batch failure until an innocent change's landing run completes, serial at depth one.\n\n")
	b.WriteString("| workload | policy | failed batches | runs/failed batch | Δ runs | runner-min/failed batch | innocent delay (min) | Δ delay | false culprits |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, row := range r.Isolation {
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s | %d |\n", row.Workload, row.Policy, row.Cases,
			f(row.RunsPerFailedBatch, 2), d(row.DeltaRunsPercent), f(row.MinutesPerFailedBatch, 1), f(row.InnocentDelayMinutes, 1), d(row.DeltaInnocentDelayPct), row.FalseCulpritDequeues)
	}
	b.WriteString("\n### Isolation model calibration (halving model vs planner bisection runs, baseline)\n\nThe model claims exactness only at depth one; at depth four, bisection interacts with failed-check retries and chain invalidation.\n\n| workload | applies | seeds matching | model runs | planner runs |\n|---|---|---|---|---|\n")
	applies := map[string]bool{}
	for _, w := range r.Workloads {
		applies[w["name"].(string)], _ = w["calibrated"].(bool)
	}
	type agg struct{ match, n, model, real int }
	order := []string{}
	cal := map[string]*agg{}
	for _, c := range r.Calibration {
		a := cal[c.Workload]
		if a == nil {
			a = &agg{}
			cal[c.Workload] = a
			order = append(order, c.Workload)
		}
		a.n++
		a.model += c.Model
		a.real += c.Real
		if c.Model == c.Real {
			a.match++
		}
	}
	for _, w := range order {
		a := cal[w]
		fmt.Fprintf(&b, "| %s | %s | %d/%d | %d | %d |\n", w, map[bool]string{true: "yes", false: "n/a"}[applies[w]], a.match, a.n, a.model, a.real)
	}
	return b.String()
}
