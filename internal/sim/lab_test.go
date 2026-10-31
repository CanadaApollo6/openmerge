package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// labDay is a small loaded depth-one workload: 120 changes in two hours.
func labDay() Config {
	cfg := DefaultConfig()
	cfg.PRs, cfg.Seed, cfg.ArrivalWindow = 120, 7, 2*time.Hour
	cfg.Planner.BatchMaxSize = 8
	cfg.Planner.BatchMaxWait = 30 * time.Second
	cfg.CI = &CIConfig{}
	cfg.Lab = &LabConfig{Risk: RiskModel{Kind: "mixture", LowRate: .015, HighRate: .25, HighFr: .15, ScoreNoise: .5}}
	return cfg
}

func labDepth4() Config {
	cfg := labDay()
	cfg.FlakeRate = .01
	cfg.Planner.CheckTimeout = 3 * time.Minute
	cfg.Planner.SpeculationMaxDepth = 4
	cfg.CI = &CIConfig{Runners: 50, CancellationSupported: true, RerunsSupported: true}
	return cfg
}

func mustRun(t *testing.T, cfg Config) Result {
	t.Helper()
	_, r := runScenario(t, cfg)
	return r
}

func withoutLab(r Result) Result {
	r.Lab = nil
	r.Config.Lab = nil
	return r
}

// With no lab strategy and the legacy risk and duration models, the lab only
// observes: every outcome, draw, timing and metric matches the plain CI run.
func TestLabInstrumentationIsTransparent(t *testing.T) {
	t.Parallel()
	for name, base := range map[string]Config{"depth-1": labDay(), "depth-4": labDepth4()} {
		t.Run(name, func(t *testing.T) {
			base.Lab = nil
			lab := base
			lab.Lab = &LabConfig{}
			plain, observed := mustRun(t, base), withoutLab(mustRun(t, lab))
			a, _ := json.Marshal(plain)
			b, _ := json.Marshal(observed)
			if string(a) != string(b) {
				t.Fatalf("lab instrumentation changed the run:\nplain %s\nlab   %s", a, b)
			}
		})
	}
}

// The ledger must agree with the existing CI accounting it extends.
func TestLabLedgerReconcilesWithCIMetrics(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{"depth-1": labDay(), "depth-4": labDepth4()} {
		for _, dedupe := range []bool{false, true} {
			cfg := cfg
			lab := *cfg.Lab
			lab.Strategy.TreeDedupe = dedupe
			cfg.Lab = &lab
			r := mustRun(t, cfg)
			m := r.Lab
			if m.Runs != r.CI.RunsStarted || m.WastedRuns != r.CI.WastedRuns || math.Abs(m.RunnerMinutes-r.CI.RunnerMinutes) > 1e-9 {
				t.Fatalf("%s dedupe=%v: ledger %d runs/%d wasted/%.3f min, CI %d/%d/%.3f", name, dedupe, m.Runs, m.WastedRuns, m.RunnerMinutes, r.CI.RunsStarted, r.CI.WastedRuns, r.CI.RunnerMinutes)
			}
			parts := m.FailedMinutes + m.InvalidatedMinutes + m.CancelledResidualMinutes + m.SupersededMinutes
			if math.Abs(parts-m.WastedMinutes) > 1e-9 || m.WastedMinutes > m.RunnerMinutes+1e-9 {
				t.Fatalf("%s: waste causes %.3f do not sum to %.3f", name, parts, m.WastedMinutes)
			}
			if !dedupe && m.DedupeHits != 0 || m.Landed != r.Merged || m.Landed == 0 {
				t.Fatalf("%s dedupe=%v: unexpected ledger %+v", name, dedupe, m)
			}
		}
	}
}

func TestLabDeterministicPerSeed(t *testing.T) {
	t.Parallel()
	cfg := labDepth4()
	cfg.Lab.Risk = RiskModel{Kind: "beta", Alpha: .5, Beta: 9.5, ScoreNoise: .5}
	cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: time.Minute, Sigma: .3, FailFastFraction: .3}
	cfg.Lab.Bursts = BurstModel{Fraction: .5, Size: 10, Spread: time.Minute}
	cfg.Lab.Strategy = LabStrategy{TreeDedupe: true, RiskBatchTarget: .8}
	first, _ := json.Marshal(mustRun(t, cfg))
	second, _ := json.Marshal(mustRun(t, cfg))
	if string(first) != string(second) {
		t.Fatal("same seed produced different lab results")
	}
	cfg.Seed++
	third, _ := json.Marshal(mustRun(t, cfg))
	if string(first) == string(third) {
		t.Fatal("seed did not affect the lab workload")
	}
}

// Without flakes a tree's result is a function of its contents, so reusing it
// must not change any outcome: every passing change lands and every broken one
// is dequeued, as in the baseline, with fewer runs.
func TestTreeDedupePreservesOutcomes(t *testing.T) {
	t.Parallel()
	hits, duplicates, baseRuns, dedupeRuns := 0, 0, 0, 0
	for _, seed := range []uint64{1, 2, 3} {
		base := labDay()
		base.Seed = seed
		dedupe := base
		lab := *base.Lab
		lab.Strategy.TreeDedupe = true
		dedupe.Lab = &lab
		b, d := mustRun(t, base), mustRun(t, dedupe)
		hits, duplicates, baseRuns, dedupeRuns = hits+d.Lab.DedupeHits, duplicates+b.Lab.DuplicateTreeRuns, baseRuns+b.Lab.Runs, dedupeRuns+d.Lab.Runs
		for _, r := range []Result{b, d} {
			if r.Merged != r.PRs-r.Broken || r.Dequeued[queue.ChecksFailed] != r.Broken || r.Lab.WrongfulDequeues != 0 {
				t.Fatalf("seed %d: outcomes changed: %+v", seed, r)
			}
		}
	}
	if hits == 0 || dedupeRuns >= baseRuns || duplicates == 0 {
		t.Fatalf("dedupe saved nothing: %d hits, %d runs vs %d, %d baseline duplicate trees", hits, dedupeRuns, baseRuns, duplicates)
	}
}

func TestRiskSizedBatchesFollowScores(t *testing.T) {
	t.Parallel()
	cfg := labDay()
	// A constant 5% score gives k = 4: 0.95^4 >= 0.8 > 0.95^5.
	cfg.Lab.Risk = RiskModel{}
	cfg.BrokenRate = .05
	cfg.Lab.Strategy.RiskBatchTarget = .8
	cfg.ArrivalWindow = 20 * time.Minute
	s, r := runScenario(t, cfg)
	largest := 0
	for _, e := range s.events {
		if started, ok := e.Event.(queue.BatchStarted); ok {
			largest = max(largest, len(started.PRs))
		}
	}
	if largest != 4 || r.Merged != r.PRs-r.Broken {
		t.Fatalf("largest risk-sized batch %d, want 4; result %+v", largest, r)
	}
}

func TestFailFastShortensOnlyFailingRuns(t *testing.T) {
	t.Parallel()
	cfg := labDay()
	cfg.Lab.Duration.FailFastFraction = .25
	s, _ := runScenario(t, cfg)
	failed := 0
	for _, job := range s.ciJobs {
		want := 2 * time.Minute
		if !job.success {
			want = 30 * time.Second
			failed++
		}
		if job.duration != want {
			t.Fatalf("run %s success=%v lasted %s, want %s", job.sha, job.success, job.duration, want)
		}
	}
	if failed == 0 {
		t.Fatal("no failing run exercised")
	}
}

func TestBurstArrivalsCluster(t *testing.T) {
	cfg := labDay()
	cfg.ArrivalWindow = 24 * time.Hour
	cfg.Lab.Bursts = BurstModel{Fraction: 1, Size: 20, Spread: time.Minute}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.lab.burstCenters) != 6 {
		t.Fatalf("got %d bursts, want 6", len(s.lab.burstCenters))
	}
	for _, e := range s.scheduled {
		if e.kind != arrival || s.prs[e.pr].untrusted {
			continue
		}
		at := e.at.Sub(s.start)
		inside := false
		for _, c := range s.lab.burstCenters {
			inside = inside || at >= c && at < c+time.Minute
		}
		if !inside {
			t.Fatalf("PR %d arrives at %s outside every burst", e.pr, at)
		}
	}
}

func TestBetaDrawMean(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	sum := 0.0
	for range 20000 {
		sum += betaDraw(r, .4, 7.6)
	}
	if mean := sum / 20000; math.Abs(mean-.05) > .003 {
		t.Fatalf("Beta(0.4, 7.6) mean %.4f, want 0.05", mean)
	}
}

func TestLabValidation(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"no CI":          func(c *Config) { c.CI = nil },
		"risk kind":      func(c *Config) { c.Lab.Risk.Kind = "normal" },
		"beta shape":     func(c *Config) { c.Lab.Risk = RiskModel{Kind: "beta"} },
		"mixture rate":   func(c *Config) { c.Lab.Risk.HighRate = 2 },
		"duration kind":  func(c *Config) { c.Lab.Duration.Kind = "pareto" },
		"lognormal":      func(c *Config) { c.Lab.Duration = DurationModel{Kind: "lognormal"} },
		"fail-fast":      func(c *Config) { c.Lab.Duration.FailFastFraction = 1.5 },
		"burst size":     func(c *Config) { c.Lab.Bursts = BurstModel{Fraction: .5} },
		"risk target":    func(c *Config) { c.Lab.Strategy.RiskBatchTarget = 1 },
		"negative noise": func(c *Config) { c.Lab.Risk.ScoreNoise = -1 },
		"repush rate":    func(c *Config) { c.Lab.Invalidation.RepushRate = 1.5 },
		"no delay":       func(c *Config) { c.Lab.Invalidation.DequeueRate = .1 },
		"delay kind": func(c *Config) {
			c.Lab.Invalidation = InvalidationModel{RepushRate: .1, Delay: DelayModel{Kind: "pareto", Mean: time.Minute}}
		},
	} {
		cfg := labDay()
		change(&cfg)
		if _, err := Run(cfg); err == nil {
			t.Errorf("%s: invalid lab config accepted", name)
		}
	}
}

func TestIsolationModel(t *testing.T) {
	culpritLast := IsolationCase{Members: []int{1, 2, 3, 4, 5, 6, 7, 8}, Broken: make([]bool, 8), Scores: []float64{.01, .01, .01, .01, .01, .01, .01, .9}}
	culpritLast.Broken[7] = true
	for _, tc := range []struct {
		name         string
		policy       IsolationPolicy
		runs, hits   int
		innocentWall float64
	}{
		// [0-3] pass, [4-7] fail, [4,5] pass, [6,7] fail, [6] pass, [7] fail.
		{name: "halving", runs: 6, innocentWall: 4*1 + 2*3 + 1*5},
		// The failing tests are identical trees to the failed batch.
		{name: "halving dedupe", policy: IsolationPolicy{Dedupe: true}, runs: 3, hits: 3, innocentWall: 4*1 + 2*2 + 1*3},
		// [7] fails, then the remainder passes.
		{name: "culprit first", policy: IsolationPolicy{CulpritFirst: true}, runs: 2, innocentWall: 7 * 2},
	} {
		cost := Isolate(culpritLast, tc.policy, time.Minute, time.Minute)
		if cost.Runs != tc.runs || cost.DedupeHits != tc.hits || cost.Culprits != 1 || cost.Innocents != 7 || cost.InnocentDelayMinutes != tc.innocentWall || cost.FalseCulpritDequeues+cost.MissedCulpritLandings != 0 {
			t.Errorf("%s: %+v", tc.name, cost)
		}
	}
	// Wrong scores cost more than halving: the top suspect passes, then the
	// remainder is isolated again.
	wrong := culpritLast
	wrong.Scores = []float64{.9, .01, .01, .01, .01, .01, .01, .01}
	if cost := Isolate(wrong, IsolationPolicy{CulpritFirst: true}, time.Minute, time.Minute); cost.Runs <= 6 || cost.Culprits != 1 {
		t.Errorf("misleading scores: %+v", cost)
	}
	// Two culprits are both isolated by every policy.
	two := IsolationCase{Members: []int{1, 2, 3, 4}, Broken: []bool{true, false, false, true}, Scores: []float64{.3, .1, .1, .3}}
	for _, p := range []IsolationPolicy{{}, {Dedupe: true}, {CulpritFirst: true}, {CulpritFirst: true, Dedupe: true}} {
		if cost := Isolate(two, p, time.Minute, time.Minute); cost.Culprits != 2 || cost.Innocents != 2 {
			t.Errorf("%+v: %+v", p, cost)
		}
	}
	// Fail-fast charges failing runs at the shorter duration.
	if cost := Isolate(culpritLast, IsolationPolicy{}, time.Minute, 15*time.Second); cost.Minutes != 3+3*.25 {
		t.Errorf("fail-fast minutes %.2f", cost.Minutes)
	}
}

// At depth one without flakes the halving model is an exact replay of the
// planner's bisection: it predicts every bisection CI run.
func TestIsolationModelCalibration(t *testing.T) {
	t.Parallel()
	seeds := []uint64{1, 2, 3}
	if testing.Short() {
		seeds = seeds[:1]
	}
	for _, seed := range seeds {
		cfg := labDay()
		cfg.Seed = seed
		r := mustRun(t, cfg)
		if r.BisectionCIRuns == 0 || !r.Lab.ModelCalibrationMatches {
			t.Fatalf("seed %d: model %d bisection runs, planner %d", seed, r.Lab.ModelHalvingBisectRuns, r.BisectionCIRuns)
		}
	}
}

// Mid-flight invalidation re-pushes and dequeues changes after their batch has
// started; with cancellation the invalidated runs stop early, and without it
// they run to completion and cost more.
func TestLabMidFlightInvalidation(t *testing.T) {
	t.Parallel()
	cfg := labDepth4()
	cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 2 * time.Minute, Sigma: .6}
	cfg.Planner.CheckTimeout = 6 * time.Minute
	cfg.Lab.Invalidation = InvalidationModel{RepushRate: .3, DequeueRate: .1, Delay: DelayModel{Kind: "uniform", Mean: time.Minute}}
	on := mustRun(t, cfg)
	off := cfg
	noCancel(&off)
	without := mustRun(t, off)
	for name, r := range map[string]Result{"on": on, "off": without} {
		if r.Dequeued[queue.HeadChanged] == 0 || r.Dequeued[queue.Requested] == 0 {
			t.Fatalf("%s: no mid-flight invalidation: %v", name, r.Dequeued)
		}
	}
	if on.CI.CancellationsConfirmed == 0 || on.Lab.CancelledResidualMinutes == 0 || without.CI.CancellationsConfirmed != 0 {
		t.Fatalf("cancellation not exercised: on %+v, off %+v", on.CI, without.CI)
	}
	if without.Lab.RunnerMinutesPerLanded <= on.Lab.RunnerMinutesPerLanded {
		t.Fatalf("cancellation saved nothing: on %.3f, off %.3f min/landed", on.Lab.RunnerMinutesPerLanded, without.Lab.RunnerMinutesPerLanded)
	}
}

// A run's duration depends on the seed, its tree and its attempt only, so arms
// that skip runs still see the same durations for the same trees.
func TestLabDurationsArePairedByTree(t *testing.T) {
	t.Parallel()
	cfg := labDay()
	cfg.Lab.Duration = DurationModel{Kind: "lognormal", Median: 2 * time.Minute, Sigma: .6}
	durations := func(dedupe bool) map[string]time.Duration {
		c := cfg
		lab := *cfg.Lab
		lab.Strategy.TreeDedupe = dedupe
		c.Lab = &lab
		s, _ := runScenario(t, c)
		out := map[string]time.Duration{}
		for _, job := range s.ciJobs {
			if job.lab != nil && !job.lab.deduped {
				out[fmt.Sprintf("%s/%d", job.lab.tree, job.attempt)] = job.duration
			}
		}
		return out
	}
	base, dedupe := durations(false), durations(true)
	shared, distinct := 0, map[time.Duration]bool{}
	for key, d := range dedupe {
		distinct[d] = true
		if b, ok := base[key]; ok {
			shared++
			if b != d {
				t.Fatalf("tree %s: baseline duration %s, dedupe arm %s", key, b, d)
			}
		}
	}
	if shared < len(dedupe)/2 || len(distinct) < 10 {
		t.Fatalf("too little overlap to test pairing: %d shared of %d, %d distinct durations", shared, len(dedupe), len(distinct))
	}
}

// With a full batch waiting, the backlog guard keeps the configured maximum
// that risk sizing would otherwise shrink.
func TestBacklogGuardKeepsMaximum(t *testing.T) {
	t.Parallel()
	largest := func(guard bool) int {
		cfg := labDay()
		cfg.Lab.Risk = RiskModel{}
		cfg.BrokenRate = .05
		cfg.Lab.Strategy = LabStrategy{RiskBatchTarget: .8, BacklogGuard: guard}
		cfg.ArrivalWindow = 20 * time.Minute
		s, r := runScenario(t, cfg)
		if r.Merged != r.PRs-r.Broken {
			t.Fatalf("guard=%v: outcomes changed: %+v", guard, r)
		}
		n := 0
		for _, e := range s.events {
			if started, ok := e.Event.(queue.BatchStarted); ok {
				n = max(n, len(started.PRs))
			}
		}
		return n
	}
	if unguarded, guarded := largest(false), largest(true); unguarded != 4 || guarded != 8 {
		t.Fatalf("largest batch %d unguarded, %d guarded; want 4 and 8", unguarded, guarded)
	}
}

// Score noise is drawn on its own stream: changing it alters scores only, so a
// baseline that ignores scores is identical at every noise level.
func TestScoreNoiseKeepsTruth(t *testing.T) {
	t.Parallel()
	run := func(noise float64) []byte {
		cfg := labDay()
		cfg.Lab.Risk.ScoreNoise = noise
		r := mustRun(t, cfg)
		r.Config.Lab = nil
		data, _ := json.Marshal(r)
		return data
	}
	if a, b := run(0), run(2); string(a) != string(b) {
		t.Fatal("score noise changed the baseline workload")
	}
}
