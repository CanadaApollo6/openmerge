package sim

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// LabConfig enables the CI efficiency lab. It is simulator-only: nil preserves
// every legacy draw, timing and JSON byte, and no field reaches the planner,
// executor or store except through the documented BatchMaxSize override below.
// The lab requires Config.CI so every run has a ledger entry.
//
// Lab draws never touch the legacy streams, so arrivals, flakes and faults keep
// their legacy draws, and every strategy over the same seed sees the same
// changes (common random numbers). Truth (risk, brokenness, bursts) uses PCG
// (seed, 8); score noise uses (seed, 10) so a noise sweep keeps the truth; and
// invalidations use (seed, 9). Each run's duration is derived from the seed,
// the tested tree and the attempt number, so a strategy that skips a run (tree
// dedupe) does not shift the durations of later runs.
type LabConfig struct {
	Risk         RiskModel
	Duration     DurationModel
	Bursts       BurstModel
	Invalidation InvalidationModel
	Strategy     LabStrategy
}

// InvalidationModel adds mid-flight invalidation sources that the legacy
// RepushRate (which fires within a second of arrival) does not exercise. Each
// trusted change independently draws whether it will be re-pushed and whether
// its author will request a dequeue; the clock starts when the change first
// enters a started batch, and the event fires Delay later if the change is
// still queued. A re-push changes the head (head-changed dequeue and a
// re-enqueue); a dequeue request removes the change for good.
type InvalidationModel struct {
	RepushRate, DequeueRate float64
	Delay                   DelayModel
}

// DelayModel is "uniform" on [0, 2·Mean) or "exponential" with Mean, clamped to
// 10·Mean. Every draw is at least one nanosecond.
type DelayModel struct {
	Kind string
	Mean time.Duration
}

func (d DelayModel) draw(r *rand.Rand) time.Duration {
	x := r.Float64()
	var f float64
	switch d.Kind {
	case "exponential":
		f = min(-math.Log(1-x), 10)
	default:
		f = 2 * x
	}
	return max(time.Nanosecond, time.Duration(f*float64(d.Mean)))
}

// RiskModel draws a true failure probability per trusted change. Kind "" keeps
// the global Config.BrokenRate draw. Score is the strategy-visible estimate: the
// true probability perturbed by Gaussian noise of ScoreNoise in logit space, a
// stand-in for ADR 0014 risk facts that are correlated with, but not equal to,
// the truth.
type RiskModel struct {
	Kind                      string // "", "beta" or "mixture"
	Alpha, Beta               float64
	LowRate, HighRate, HighFr float64
	ScoreNoise                float64
}

// DurationModel draws a CI service time per run. Kind "" keeps the legacy
// CIDuration/CIMin/CIMax draw. "lognormal" uses Median·exp(Sigma·Z), clamped to
// [Median/4, 3·Median]. FailFastFraction in (0, 1) ends failing runs after that
// fraction of their drawn duration; zero or one disables fail-fast.
type DurationModel struct {
	Kind             string
	Median           time.Duration
	Sigma            float64
	FailFastFraction float64
}

// BurstModel moves Fraction of trusted changes into bursts, modelling agents
// that open many changes at once. Bursts have Size changes on average, start
// uniformly in the arrival window, and spread each burst over Spread.
type BurstModel struct {
	Fraction float64
	Size     int
	Spread   time.Duration
}

// LabStrategy selects simulator-side strategies. None changes planner code.
//
// TreeDedupe reuses the latest completed result for a test commit whose tree is
// byte-identical to an earlier completed run: the run costs no runner time and
// completes at once. Explicit reruns of the same SHA are never deduplicated.
//
// RiskBatchTarget > 0 overrides Planner.BatchMaxSize on each planning pass with
// the largest k such that the product of (1-score) over the next k unbatched
// changes stays at or above the target. It never exceeds Planner.BatchMaxSize.
// When every waiting change fits, the configured maximum is passed unchanged so
// the planner's own BatchMaxWait logic is preserved.
//
// BacklogGuard keeps the configured BatchMaxSize, disabling both risk sizing
// and the production BatchTargetPassRate for that pass, whenever at least
// BatchMaxSize unbatched trusted changes are waiting. A full batch's worth of
// backlog is a queue-observable sign of high utilisation, where shrinking fresh
// batches cuts serial throughput. It is the sim-only rule the production guard
// (queue.BacklogGuardSize, on whenever a target is set unless
// Planner.BatchBacklogGuardOff) was ported from, kept for comparison.
type LabStrategy struct {
	TreeDedupe      bool
	RiskBatchTarget float64
	BacklogGuard    bool
}

func (c LabConfig) validate(cfg Config) error {
	if cfg.CI == nil {
		return fmt.Errorf("lab requires a CI workload so every run is accounted")
	}
	bad := func(x float64) bool { return math.IsNaN(x) || math.IsInf(x, 0) }
	r := c.Risk
	switch r.Kind {
	case "":
	case "beta":
		if bad(r.Alpha) || bad(r.Beta) || r.Alpha <= 0 || r.Beta <= 0 {
			return fmt.Errorf("beta risk requires positive alpha and beta")
		}
	case "mixture":
		for _, p := range []float64{r.LowRate, r.HighRate, r.HighFr} {
			if bad(p) || p < 0 || p > 1 {
				return fmt.Errorf("mixture risk rates must be between 0 and 1")
			}
		}
	default:
		return fmt.Errorf("unknown risk model %q", r.Kind)
	}
	if bad(r.ScoreNoise) || r.ScoreNoise < 0 {
		return fmt.Errorf("risk score noise must be nonnegative")
	}
	d := c.Duration
	switch d.Kind {
	case "":
	case "lognormal":
		if d.Median <= 0 || bad(d.Sigma) || d.Sigma < 0 {
			return fmt.Errorf("lognormal duration requires a positive median and nonnegative sigma")
		}
	default:
		return fmt.Errorf("unknown duration model %q", d.Kind)
	}
	if bad(d.FailFastFraction) || d.FailFastFraction < 0 || d.FailFastFraction > 1 {
		return fmt.Errorf("fail-fast fraction must be between 0 and 1")
	}
	b := c.Bursts
	if bad(b.Fraction) || b.Fraction < 0 || b.Fraction > 1 || b.Fraction > 0 && b.Size < 1 || b.Spread < 0 {
		return fmt.Errorf("invalid burst model")
	}
	v := c.Invalidation
	if bad(v.RepushRate) || bad(v.DequeueRate) || v.RepushRate < 0 || v.RepushRate > 1 || v.DequeueRate < 0 || v.DequeueRate > 1 {
		return fmt.Errorf("invalidation rates must be between 0 and 1")
	}
	if v.RepushRate+v.DequeueRate > 0 && (v.Delay.Mean <= 0 || v.Delay.Kind != "uniform" && v.Delay.Kind != "exponential") {
		return fmt.Errorf("invalidation requires a uniform or exponential delay with a positive mean")
	}
	t := c.Strategy.RiskBatchTarget
	if bad(t) || t < 0 || t >= 1 {
		return fmt.Errorf("risk batch target must be in [0, 1)")
	}
	return nil
}

// MarshalJSON records lab settings with durations in seconds.
func (c LabConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"risk": map[string]any{"kind": c.Risk.Kind, "alpha": c.Risk.Alpha, "beta": c.Risk.Beta,
			"low_rate": c.Risk.LowRate, "high_rate": c.Risk.HighRate, "high_fraction": c.Risk.HighFr, "score_noise": c.Risk.ScoreNoise},
		"duration": map[string]any{"kind": c.Duration.Kind, "median_seconds": c.Duration.Median.Seconds(),
			"sigma": c.Duration.Sigma, "fail_fast_fraction": c.Duration.FailFastFraction},
		"bursts": map[string]any{"fraction": c.Bursts.Fraction, "size": c.Bursts.Size, "spread_seconds": c.Bursts.Spread.Seconds()},
		"invalidation": map[string]any{"repush_rate": c.Invalidation.RepushRate, "dequeue_rate": c.Invalidation.DequeueRate,
			"delay_kind": c.Invalidation.Delay.Kind, "delay_mean_seconds": c.Invalidation.Delay.Mean.Seconds()},
		"strategy": map[string]any{"tree_dedupe": c.Strategy.TreeDedupe, "risk_batch_target": c.Strategy.RiskBatchTarget, "backlog_guard": c.Strategy.BacklogGuard},
	})
}

// labState is per-simulation lab bookkeeping. trees holds the latest completed
// result per tree; seen records every tree that has completed at least once.
type labState struct {
	rng, scores, invalidations *rand.Rand
	burstCenters               []time.Duration
	trees                      map[string]bool
}

// labInvalidation is one change's pending mid-flight invalidation. Zero delays
// mean none; armed is set when the change first enters a started batch.
type labInvalidation struct {
	repushIn, dequeueIn time.Duration
	armed               bool
}

// labJob tags a ledger entry. A deduplicated job never occupies a runner.
type labJob struct {
	tree           string
	batchID        string
	prs            []int
	bisection      bool
	onPredecessor  bool
	deduped        bool
	duplicateTree  bool
	firstAttempt   bool
	plannedSuccess bool
}

func newLabState(cfg Config) *labState {
	l := &labState{rng: rand.New(rand.NewPCG(cfg.Seed, 8)), scores: rand.New(rand.NewPCG(cfg.Seed, 10)),
		invalidations: rand.New(rand.NewPCG(cfg.Seed, 9)), trees: make(map[string]bool)}
	if b := cfg.Lab.Bursts; b.Fraction > 0 && cfg.ArrivalWindow > 0 {
		n := max(1, int(math.Round(b.Fraction*float64(cfg.PRs)/float64(b.Size))))
		for range n {
			l.burstCenters = append(l.burstCenters, time.Duration(l.rng.Int64N(int64(cfg.ArrivalWindow))))
		}
	}
	return l
}

// drawChange assigns true risk, score, brokenness and arrival for one trusted
// change. legacyBroken and legacyDelay are the already-drawn legacy values.
func (s *simulation) drawChange(p *pullRequest, legacyBroken bool, legacyDelay time.Duration) time.Duration {
	lab, cfg := s.lab, s.cfg.Lab
	p.risk = s.cfg.BrokenRate
	switch cfg.Risk.Kind {
	case "beta":
		p.risk = betaDraw(lab.rng, cfg.Risk.Alpha, cfg.Risk.Beta)
	case "mixture":
		p.risk = cfg.Risk.LowRate
		if lab.rng.Float64() < cfg.Risk.HighFr {
			p.risk = cfg.Risk.HighRate
		}
	}
	p.broken = legacyBroken
	if cfg.Risk.Kind != "" {
		p.broken = lab.rng.Float64() < p.risk
	}
	// Draw the noise unconditionally so every noise level sees the same z.
	z := lab.scores.NormFloat64()
	p.score = p.risk
	if cfg.Risk.ScoreNoise > 0 {
		p.score = sigmoid(logit(p.risk) + cfg.Risk.ScoreNoise*z)
	}
	// Likewise draw every invalidation variate, so rate sweeps stay paired.
	v := cfg.Invalidation
	repush, dequeue := lab.invalidations.Float64(), lab.invalidations.Float64()
	repushIn, dequeueIn := v.Delay.draw(lab.invalidations), v.Delay.draw(lab.invalidations)
	if repush < v.RepushRate || dequeue < v.DequeueRate {
		p.labInvalidation = &labInvalidation{}
		if repush < v.RepushRate {
			p.labInvalidation.repushIn = repushIn
		}
		if dequeue < v.DequeueRate {
			p.labInvalidation.dequeueIn = dequeueIn
		}
	}
	delay := legacyDelay
	if len(lab.burstCenters) > 0 && lab.rng.Float64() < cfg.Bursts.Fraction {
		delay = lab.burstCenters[lab.rng.IntN(len(lab.burstCenters))]
		if cfg.Bursts.Spread > 0 {
			delay += time.Duration(lab.rng.Int64N(int64(cfg.Bursts.Spread)))
		}
		delay = min(delay, s.cfg.ArrivalWindow)
	}
	return delay
}

func logit(p float64) float64 {
	p = min(max(p, 1e-6), 1-1e-6)
	return math.Log(p / (1 - p))
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// betaDraw uses two Marsaglia-Tsang gamma draws.
func betaDraw(r *rand.Rand, a, b float64) float64 {
	x := gammaDraw(r, a)
	y := gammaDraw(r, b)
	if x+y == 0 {
		return 0
	}
	return x / (x + y)
}

func gammaDraw(r *rand.Rand, shape float64) float64 {
	if shape < 1 {
		return gammaDraw(r, shape+1) * math.Pow(r.Float64(), 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// runDuration draws a run's service time. Legacy workloads use ciDuration
// unchanged. Lognormal draws come from a generator keyed by the seed, the tested
// tree and the attempt, so the same tree's nth attempt has the same duration in
// every strategy arm regardless of how many runs another arm skipped. Fail-fast
// applies to both.
func (s *simulation) runDuration(tree string, attempt uint64, success bool) time.Duration {
	if s.lab == nil {
		return s.ciDuration()
	}
	m := s.cfg.Lab.Duration
	var d time.Duration
	switch m.Kind {
	case "lognormal":
		f := math.Exp(m.Sigma * runRNG(s.cfg.Seed, tree, attempt).NormFloat64())
		f = min(max(f, 0.25), 3)
		d = time.Duration(float64(m.Median) * f)
	default:
		d = s.ciDuration()
	}
	if !success && m.FailFastFraction > 0 && m.FailFastFraction < 1 {
		d = max(time.Second, time.Duration(float64(d)*m.FailFastFraction))
	}
	return d
}

// runRNG is a generator for one (seed, tree, attempt) run identity.
func runRNG(seed uint64, tree string, attempt uint64) *rand.Rand {
	h := sha256.Sum256(fmt.Appendf(nil, "%d/%s/%d", seed, tree, attempt))
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(h[:8]), binary.BigEndian.Uint64(h[8:16])))
}

// rerunDuration draws the duration of an explicit same-SHA rerun from the
// previous attempt's ledger tag.
func (s *simulation) rerunDuration(sha string, success bool) time.Duration {
	run := s.tests[sha]
	if s.lab == nil || run == nil || run.job == nil || run.job.lab == nil {
		return s.runDuration("", 0, success)
	}
	return s.runDuration(run.job.lab.tree, run.job.attempt+1, success)
}

// labBatchStarted arms each member's pending invalidation the first time it
// enters a started batch.
func (s *simulation) labBatchStarted(prs []queue.BatchPR) {
	if s.lab == nil {
		return
	}
	for _, pr := range prs {
		v := s.prs[pr.PR].labInvalidation
		if v == nil || v.armed {
			continue
		}
		v.armed = true
		if v.repushIn > 0 {
			s.schedule(s.now.Add(v.repushIn), repush, pr.PR, "")
		}
		if v.dequeueIn > 0 {
			s.schedule(s.now.Add(v.dequeueIn), labDequeue, pr.PR, "")
		}
	}
}

// treeKey hashes the exact file tree of a test commit. Commit SHAs differ for
// identical trees (different parents and messages); trees do not.
func (s *simulation) treeKey(sha string) (string, error) {
	c, err := s.forge.Commit(s.repo, sha)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(c.Files))
	for path := range c.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(h, "%d:%s%d:%s", len(path), path, len(c.Files[path]), c.Files[path])
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// labSubmit tags and submits a first-attempt run. With TreeDedupe and a known
// tree, the cached result completes at the current instant without a runner.
func (s *simulation) labSubmit(sha string, run *testRun, batch *queue.Batch) {
	tree, err := s.treeKey(sha)
	if err != nil {
		s.violation = err
		return
	}
	prs := make([]int, len(batch.PRs))
	for i, pr := range batch.PRs {
		prs[i] = pr.PR
	}
	tag := &labJob{tree: tree, batchID: batch.ID, prs: prs, bisection: batch.FromBisection, onPredecessor: batch.ParentBatchID != "", firstAttempt: true}
	cached, known := s.lab.trees[tree]
	tag.duplicateTree = known
	if known && s.cfg.Lab.Strategy.TreeDedupe {
		run.success = cached
		tag.deduped = true
		tag.plannedSuccess = cached
		s.submitDeduped(sha, tag)
		return
	}
	tag.plannedSuccess = run.success
	s.submitCI(sha, s.runDuration(tree, 1, run.success))
	run.job.lab = tag
}

// submitDeduped records a zero-length ledger entry. It is momentarily counted
// as running so every completion and cancellation path stays balanced, but it
// is not a CI run and never waits for or consumes runner capacity.
func (s *simulation) submitDeduped(sha string, tag *labJob) {
	job := &ciJob{id: 1, attempt: 1, sha: sha, success: tag.plannedSuccess, lab: tag, started: s.now}
	s.tests[sha].job = job
	s.ciJobs = append(s.ciJobs, job)
	s.ciRunning++
	s.sequence++
	heap.Push(&s.scheduled, scheduledEvent{at: s.now, sequence: s.sequence, kind: ciComplete, sha: sha, job: job})
}

func (s *simulation) labCompleted(job *ciJob, success bool) {
	if s.lab == nil || job.lab == nil || job.cancelled {
		return
	}
	s.lab.trees[job.lab.tree] = success
}

// plannerConfig is the configuration passed to every planning call. Without a
// risk-sizing strategy it is exactly cfg.Planner.
func (s *simulation) plannerConfig() queue.Config {
	cfg := s.cfg.Planner
	if s.lab == nil {
		return cfg
	}
	strategy := s.cfg.Lab.Strategy
	if strategy.RiskBatchTarget == 0 && !strategy.BacklogGuard {
		return cfg
	}
	waiting := s.waitingTrusted()
	if strategy.BacklogGuard && len(waiting) >= max(1, cfg.BatchMaxSize) {
		cfg.BatchTargetPassRate = 0
		return cfg
	}
	if strategy.RiskBatchTarget > 0 {
		cfg.BatchMaxSize = s.riskBatchSize(waiting)
	}
	return cfg
}

// waitingTrusted lists unbatched trusted entries in queue order.
func (s *simulation) waitingTrusted() []int {
	batched := make(map[int]bool)
	for _, b := range s.state.Batches() {
		for _, pr := range b.PRs {
			batched[pr.PR] = true
		}
	}
	var waiting []int
	for _, entry := range s.state.Entries {
		if !batched[entry.PR] && !s.prs[entry.PR].untrusted {
			waiting = append(waiting, entry.PR)
		}
	}
	return waiting
}

func (s *simulation) riskBatchSize(waiting []int) int {
	limit := max(1, s.cfg.Planner.BatchMaxSize)
	target := s.cfg.Lab.Strategy.RiskBatchTarget
	pass, k := 1.0, 0
	for _, pr := range waiting {
		pass *= 1 - s.prs[pr].score
		if k > 0 && pass < target {
			return k
		}
		k++
		if k == limit {
			return k
		}
	}
	// Every waiting change fits: keep the configured maximum so BatchMaxWait
	// still governs partial batches exactly as in production.
	return limit
}

// LabMetrics is the per-run ledger summary. Minutes are runner-minutes; a run
// contributed when it is the final, uncancelled run of a tested SHA that landed.
//
// CancelledResidualMinutes is residual waste: the elapsed time charged to runs
// before they were cancelled. It is not the time cancellation recovered; that
// is the paired difference against an arm without cancellation support.
type LabMetrics struct {
	Landed, Dequeued         int
	Runs, DedupeHits         int
	DuplicateTreeRuns        int
	RunnerMinutes            float64
	RunsPerLanded            float64
	RunnerMinutesPerLanded   float64
	WastedRuns               int
	WastedMinutes            float64
	WastedMinutesPerLanded   float64
	WasteFraction            float64
	FailedMinutes            float64
	InvalidatedMinutes       float64
	CancelledResidualMinutes float64
	SupersededMinutes        float64
	BisectionRuns            int
	BisectionMinutes         float64
	RerunRuns                int
	RerunMinutes             float64
	SpeculativeRuns          int
	SpeculativeMinutes       float64
	P50LatencyMinutes        float64
	P90LatencyMinutes        float64
	MeanLatencyMinutes       float64
	MakespanHours            float64
	ThroughputPerDay         float64
	WrongfulDequeues         int
	FailedBatches            []IsolationCase `json:"-"`
	ModelHalvingBisectRuns   int
	PlannerBisectionRuns     int
	ModelCalibrationMatches  bool
	// FreshBatches counts fresh batch starts; FreshBatchesAtWindowOne those
	// started while the admission window was 1, where the backlog guard
	// applies; GuardRaisedBatches those above the rational adaptive size. They
	// are recorded per run, not in the summary.
	FreshBatches, FreshBatchesAtWindowOne, GuardRaisedBatches int
}

func (s *simulation) labMetrics(merged int) *LabMetrics {
	m := &LabMetrics{Landed: merged}
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if p.dequeued != "" && p.landed.IsZero() {
			m.Dequeued++
			if !p.broken && (p.dequeued == queue.ChecksFailed || p.dequeued == queue.ChecksTimeout) {
				m.WrongfulDequeues++
			}
		}
	}
	for _, job := range s.ciJobs {
		if job.lab != nil && job.lab.deduped {
			m.DedupeHits++
			continue
		}
		if job.started.IsZero() {
			continue
		}
		end := job.finished
		if end.IsZero() {
			end = s.now
		}
		minutes := end.Sub(job.started).Minutes()
		m.Runs++
		m.RunnerMinutes += minutes
		if job.lab != nil && job.lab.duplicateTree {
			m.DuplicateTreeRuns++
		}
		if job.rerun {
			m.RerunRuns++
			m.RerunMinutes += minutes
		}
		if job.lab != nil && job.lab.bisection {
			m.BisectionRuns++
			m.BisectionMinutes += minutes
		}
		if job.lab != nil && job.lab.onPredecessor {
			m.SpeculativeRuns++
			m.SpeculativeMinutes += minutes
		}
		run := s.tests[job.sha]
		if run != nil && run.landed && run.job == job && !job.cancelled {
			continue
		}
		m.WastedRuns++
		m.WastedMinutes += minutes
		switch {
		case job.cancelled:
			m.CancelledResidualMinutes += minutes
		case !job.finished.IsZero() && !job.success:
			m.FailedMinutes += minutes
		case run != nil && run.landed:
			m.SupersededMinutes += minutes
		default:
			m.InvalidatedMinutes += minutes
		}
	}
	var latencies []float64
	var last time.Time
	for pr := 1; pr < len(s.prs); pr++ {
		p := s.prs[pr]
		if p.landed.IsZero() {
			continue
		}
		latencies = append(latencies, p.landed.Sub(p.enqueued).Minutes())
		if p.landed.After(last) {
			last = p.landed
		}
	}
	if merged > 0 {
		m.RunsPerLanded = float64(m.Runs) / float64(merged)
		m.RunnerMinutesPerLanded = m.RunnerMinutes / float64(merged)
		m.WastedMinutesPerLanded = m.WastedMinutes / float64(merged)
		m.P50LatencyMinutes = quantile(latencies, 0.5)
		m.P90LatencyMinutes = quantile(latencies, 0.9)
		var total float64
		for _, l := range latencies {
			total += l
		}
		m.MeanLatencyMinutes = total / float64(len(latencies))
		m.MakespanHours = last.Sub(s.start).Hours()
		if m.MakespanHours > 0 {
			m.ThroughputPerDay = float64(merged) / (m.MakespanHours / 24)
		}
	}
	if m.RunnerMinutes > 0 {
		m.WasteFraction = m.WastedMinutes / m.RunnerMinutes
	}
	m.FailedBatches = s.failedBatches()
	for _, c := range m.FailedBatches {
		m.ModelHalvingBisectRuns += Isolate(c, IsolationPolicy{}, time.Minute, time.Minute).Runs
	}
	m.FreshBatches, m.FreshBatchesAtWindowOne, m.GuardRaisedBatches = s.freshBatches, s.freshAtWindowOne, s.guardRaised
	m.PlannerBisectionRuns = s.result.BisectionCIRuns
	m.ModelCalibrationMatches = m.ModelHalvingBisectRuns == m.PlannerBisectionRuns
	return m
}

// failedBatches returns every failed, completed first-attempt run of a fresh
// multi-change batch tested on the base: the input bisection starts from at
// depth one. Provisional failures on a predecessor are excluded.
func (s *simulation) failedBatches() []IsolationCase {
	var cases []IsolationCase
	for _, job := range s.ciJobs {
		t := job.lab
		if t == nil || !t.firstAttempt || t.bisection || t.onPredecessor || len(t.prs) < 2 || job.cancelled || job.finished.IsZero() || job.success {
			continue
		}
		c := IsolationCase{Members: slices.Clone(t.prs)}
		for _, pr := range t.prs {
			c.Broken = append(c.Broken, s.prs[pr].broken)
			c.Scores = append(c.Scores, s.prs[pr].score)
		}
		cases = append(cases, c)
	}
	return cases
}

// quantile uses nearest rank, except the median, which uses the midpoint.
func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := slices.Clone(xs)
	slices.Sort(sorted)
	if q == 0.5 {
		n := len(sorted)
		if n%2 == 1 {
			return sorted[n/2]
		}
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	rank := int(math.Ceil(q*float64(len(sorted)))) - 1
	return sorted[min(max(rank, 0), len(sorted)-1)]
}

// tagRerun carries a run's ledger tag to an explicit same-SHA rerun. Reruns
// are never deduplicated: they exist to re-sample a possibly flaky result.
func (s *simulation) tagRerun(sha string) {
	run := s.tests[sha]
	if s.lab == nil || run == nil || run.job == nil {
		return
	}
	for i := len(s.ciJobs) - 2; i >= 0; i-- {
		if previous := s.ciJobs[i]; previous.sha == sha && previous.lab != nil {
			tag := *previous.lab
			tag.deduped, tag.duplicateTree, tag.firstAttempt = false, false, false
			tag.plannedSuccess = run.success
			run.job.lab = &tag
			return
		}
	}
}
