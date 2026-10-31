package sim

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Config describes trusted PRs plus one additional untrusted fork probe per 50
// PRs (at least one for a nonempty workload). Arrivals have uniform jitter in
// ArrivalWindow; enqueue follows observation by one simulated second.
type Config struct {
	// CI enables Phase 2 workload capabilities. Nil preserves the legacy workload,
	// random draws, timing, and JSON exactly.
	CI *CIConfig `json:",omitempty"`
	// Lab enables the simulator-only CI efficiency lab (lab.go). Nil preserves
	// every legacy draw, timing, and JSON byte.
	Lab             *LabConfig `json:",omitempty"`
	PRs             int
	Seed            uint64
	ArrivalWindow   time.Duration
	BrokenRate      float64
	FlakeRate       float64
	DropWebhookRate float64
	RepushRate      float64
	ConflictRate    float64
	// ExternalPushRate selects PRs accompanied by external base pushes (see doc.go).
	ExternalPushRate float64
	// CIMin/CIMax, when both positive, override fixed CIDuration.
	CIDuration  time.Duration
	CIMin       time.Duration
	CIMax       time.Duration
	MergeMethod queue.MergeMethod
	Planner     queue.Config
	Faults      Faults
	RetryDelay  time.Duration
	MaxSteps    int
}

// Faults injects errors at every forge boundary, lost landing acknowledgements,
// late pushes, duplicate Land executions, and CI reruns after success. Methods
// overrides ForgeErrorRate per method; FailFirst fails the first N calls.
type Faults struct {
	ForgeErrorRate         float64
	Methods                map[string]float64
	FailFirst              map[string]int
	LandingCrashRate       float64
	DelayedPushRate        float64
	DuplicateExecutionRate float64
	CIRerunRate            float64
	ReplayDelay            time.Duration
}

// DefaultFaults fails each method once, then exercises random transient errors
// and uncertain landings. Methods unused by the executor are tested separately.
func DefaultFaults() Faults {
	methods := make(map[string]float64, len(forgeMethods))
	first := make(map[string]int, len(forgeMethods))
	for _, method := range forgeMethods {
		methods[method] = 0.02
		first[method] = 1
	}
	return Faults{Methods: methods, FailFirst: first, LandingCrashRate: 0.02, DelayedPushRate: 0.02, DuplicateExecutionRate: 0.02}
}

func configuredForgeMethods(cfg Config) []string {
	methods := append([]string(nil), forgeMethods...)
	if cfg.Planner.RetryLimit() == 1 {
		methods = append(methods, "RerunFailedChecks")
	}
	if cfg.Planner.MaxDepth() > 1 {
		methods = append(methods, "CancelChecks")
	}
	return methods
}

// DefaultConfig models a 500-PR day. Two-minute CI leaves serial capacity for
// 720 runs/day; slower CI can legitimately drain past the arrival window.
func DefaultConfig() Config {
	return Config{
		PRs: 500, Seed: 1, ArrivalWindow: 24 * time.Hour, BrokenRate: 0.05,
		CIDuration: 2 * time.Minute,
		Planner: queue.Config{
			CheckTimeout: 30 * time.Minute, ResolveAfter: 5 * time.Second,
			RefreshInterval: time.Minute, BuildBackoff: time.Second,
		},
		RetryDelay: time.Second, MaxSteps: 1_000_000,
	}
}

// Result contains aggregate outcomes. JSON durations are floating-point seconds.
// Untrusted probes are extra PRs, counted separately from PRs and Broken.
type Result struct {
	CI                  *CIMetrics                  `json:"ci_workload,omitempty"`
	Lab                 *LabMetrics                 `json:"lab,omitempty"`
	Config              Config                      `json:"config"`
	PRs                 int                         `json:"prs"`
	Broken              int                         `json:"broken"`
	Untrusted           int                         `json:"untrusted"`
	Merged              int                         `json:"merged"`
	Dequeued            map[queue.DequeueReason]int `json:"dequeued"`
	Blocked             map[queue.DequeueReason]int `json:"blocked"`
	StillQueued         int                         `json:"still_queued"`
	MeanTimeToMerge     time.Duration               `json:"-"`
	MedianTimeToMerge   time.Duration               `json:"-"`
	P95TimeToMerge      time.Duration               `json:"-"`
	BisectionCIRuns     int                         `json:"bisection_ci_runs"`
	CIRuns              int                         `json:"ci_runs"`
	CIReruns            int                         `json:"ci_reruns"`
	CIRunsPerMergedPR   float64                     `json:"ci_runs_per_merged_pr"`
	SimulatedSpan       time.Duration               `json:"-"`
	Steps               int                         `json:"steps"`
	FaultsInjected      int                         `json:"faults_injected"`
	FaultsRecovered     int                         `json:"faults_recovered"`
	FaultsByMethod      map[string]int              `json:"faults_by_method"`
	DelayedPushes       int                         `json:"delayed_pushes"`
	DuplicateExecutions int                         `json:"duplicate_executions"`
	RejectedReplays     int                         `json:"rejected_replays"`
	DroppedWebhooks     int                         `json:"dropped_webhooks"`
	ExternalPushes      int                         `json:"external_pushes"`

	// First-enqueue latency includes time spent in earlier admissions.
	MedianTimeFromFirstEnqueue time.Duration `json:"-"`
	P95TimeFromFirstEnqueue    time.Duration `json:"-"`
}

func (cfg Config) normalized() (Config, error) {
	if cfg.CI != nil {
		if err := cfg.CI.validate(); err != nil {
			return cfg, err
		}
	}
	if cfg.Lab != nil {
		if err := cfg.Lab.validate(cfg); err != nil {
			return cfg, err
		}
	}
	if cfg.PRs < 0 || cfg.ArrivalWindow < 0 {
		return cfg, fmt.Errorf("PR count and arrival window must be nonnegative")
	}
	rates := []struct {
		name string
		rate float64
	}{
		{"broken rate", cfg.BrokenRate}, {"flake rate", cfg.FlakeRate},
		{"drop webhook rate", cfg.DropWebhookRate},
		{"repush rate", cfg.RepushRate}, {"conflict rate", cfg.ConflictRate},
		{"external push rate", cfg.ExternalPushRate},
		{"delayed push rate", cfg.Faults.DelayedPushRate}, {"duplicate execution rate", cfg.Faults.DuplicateExecutionRate},
		{"forge error rate", cfg.Faults.ForgeErrorRate}, {"landing crash rate", cfg.Faults.LandingCrashRate},
		{"CI rerun rate", cfg.Faults.CIRerunRate},
	}
	allowedMethods := configuredForgeMethods(cfg)
	for method, count := range cfg.Faults.FailFirst {
		if !slices.Contains(allowedMethods, method) || count < 0 {
			return cfg, fmt.Errorf("invalid initial fault count %q: %d", method, count)
		}
	}
	for method, rate := range cfg.Faults.Methods {
		if !slices.Contains(allowedMethods, method) {
			return cfg, fmt.Errorf("unknown forge method %q", method)
		}
		rates = append(rates, struct {
			name string
			rate float64
		}{method + " error rate", rate})
	}
	for _, r := range rates {
		if math.IsNaN(r.rate) || r.rate < 0 || r.rate > 1 {
			return cfg, fmt.Errorf("%s must be between 0 and 1", r.name)
		}
	}
	if cfg.CIDuration == 0 {
		cfg.CIDuration = 2 * time.Minute
	}
	if cfg.CIDuration < 0 || cfg.CIMin < 0 || cfg.CIMax < 0 ||
		(cfg.CIMin != 0 || cfg.CIMax != 0) && (cfg.CIMin == 0 || cfg.CIMax < cfg.CIMin) {
		return cfg, fmt.Errorf("CI duration must be positive; CI range requires 0 < min <= max")
	}
	if cfg.MergeMethod == "" {
		cfg.MergeMethod = cfg.Planner.MergeMethod
		if cfg.MergeMethod == "" {
			cfg.MergeMethod = queue.Merge
		}
	}
	switch cfg.MergeMethod {
	case queue.Merge, queue.Squash, queue.Rebase, queue.FastForward:
	default:
		return cfg, fmt.Errorf("unsupported merge method %q", cfg.MergeMethod)
	}
	cfg.Planner.MergeMethod = cfg.MergeMethod
	if cfg.Planner.MaxDepth() < 1 || cfg.Planner.MaxDepth() > 32 || cfg.Planner.SpeculationMinDepth < 0 || max(1, cfg.Planner.SpeculationMinDepth) > cfg.Planner.MaxDepth() {
		return cfg, fmt.Errorf("invalid speculation window")
	}
	if cfg.Planner.MaxDepth() > 1 {
		cfg.Planner.ChecksRetryFailed = 1
	}
	if cfg.Planner.ChecksRetryFailed != 0 && cfg.Planner.ChecksRetryFailed != 1 {
		return cfg, fmt.Errorf("checks retry failed must be 0 or 1")
	}
	if !queue.ValidTargetPassRate(cfg.Planner.BatchTargetPassRate) {
		return cfg, fmt.Errorf("invalid target pass rate")
	}
	if cfg.Planner.BatchBacklogGuardOff && cfg.Planner.BatchTargetPassRate == 0 {
		return cfg, fmt.Errorf("the backlog guard setting requires a target pass rate")
	}
	if cfg.Planner.BatchMaxSize < 0 || cfg.Planner.BatchMaxSize > 128 || cfg.Planner.BatchMaxWait < 0 {
		return cfg, fmt.Errorf("invalid planner batching settings")
	}
	if cfg.RetryDelay < 0 || cfg.MaxSteps < 0 {
		return cfg, fmt.Errorf("retry delay and step limit must be nonnegative")
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = time.Second
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 1_000_000
	}
	if cfg.Planner.RefreshInterval <= 0 {
		cfg.Planner.RefreshInterval = time.Minute
	}
	if cfg.Planner.BuildRetries <= 0 {
		cfg.Planner.BuildRetries = 3
	}
	if cfg.Faults.ReplayDelay < 0 {
		return cfg, fmt.Errorf("replay delay must be nonnegative")
	}
	if cfg.Faults.ReplayDelay == 0 {
		cfg.Faults.ReplayDelay = 2*max(cfg.Planner.ResolveAfter, 0) + 2*cfg.RetryDelay + time.Nanosecond
	}
	return cfg, nil
}

// MarshalJSON keeps Go's duration API while making exported run records readable.
func (r Result) MarshalJSON() ([]byte, error) {
	type plain Result
	return json.Marshal(struct {
		plain
		Mean        float64 `json:"mean_time_to_merge_seconds"`
		Median      float64 `json:"median_time_to_merge_seconds"`
		P95         float64 `json:"p95_time_to_merge_seconds"`
		FirstMedian float64 `json:"median_time_from_first_enqueue_seconds"`
		FirstP95    float64 `json:"p95_time_from_first_enqueue_seconds"`
		Span        float64 `json:"simulated_span_seconds"`
	}{plain(r), r.MeanTimeToMerge.Seconds(), r.MedianTimeToMerge.Seconds(), r.P95TimeToMerge.Seconds(),
		r.MedianTimeFromFirstEnqueue.Seconds(), r.P95TimeFromFirstEnqueue.Seconds(), r.SimulatedSpan.Seconds()})
}

// MarshalJSON records every effective setting with durations in seconds.
func (cfg Config) MarshalJSON() ([]byte, error) {
	planner := map[string]any{
		"batch_max_size": cfg.Planner.BatchMaxSize, "batch_max_wait_seconds": cfg.Planner.BatchMaxWait.Seconds(), "merge_method": cfg.Planner.MergeMethod, "check_timeout_seconds": cfg.Planner.CheckTimeout.Seconds(),
		"resolve_after_seconds": cfg.Planner.ResolveAfter.Seconds(), "refresh_interval_seconds": cfg.Planner.RefreshInterval.Seconds(),
		"build_retries": cfg.Planner.BuildRetries, "build_backoff_seconds": cfg.Planner.BuildBackoff.Seconds(),
	}
	// Retry support is additive to the legacy exit record. Omitting the field
	// for zero preserves the old JSON bytes exactly.
	if cfg.Planner.ChecksRetryFailed != 0 {
		planner["checks_retry_failed"] = cfg.Planner.ChecksRetryFailed
	}
	if cfg.Planner.MaxDepth() > 1 {
		planner["speculation_max_depth"] = cfg.Planner.MaxDepth()
		planner["speculation_min_depth"] = max(1, cfg.Planner.SpeculationMinDepth)
	}
	if cfg.Planner.BatchTargetPassRate > 0 {
		planner["batch_target_pass_rate"] = cfg.Planner.BatchTargetPassRate
	}
	if cfg.Planner.BatchBacklogGuardOff {
		planner["batch_backlog_guard"] = false
	}
	values := map[string]any{
		"prs": cfg.PRs, "seed": cfg.Seed, "arrival_window_seconds": cfg.ArrivalWindow.Seconds(),
		"broken_rate": cfg.BrokenRate, "flake_rate": cfg.FlakeRate, "drop_webhook_rate": cfg.DropWebhookRate,
		"repush_rate": cfg.RepushRate, "conflict_rate": cfg.ConflictRate,
		"external_push_rate":  cfg.ExternalPushRate,
		"ci_duration_seconds": cfg.CIDuration.Seconds(), "ci_min_seconds": cfg.CIMin.Seconds(), "ci_max_seconds": cfg.CIMax.Seconds(),
		"merge_method": cfg.MergeMethod, "retry_delay_seconds": cfg.RetryDelay.Seconds(), "max_steps": cfg.MaxSteps,
		"planner": planner,
		"faults": map[string]any{"forge_error_rate": cfg.Faults.ForgeErrorRate, "methods": cfg.Faults.Methods, "fail_first": cfg.Faults.FailFirst,
			"landing_crash_rate": cfg.Faults.LandingCrashRate, "delayed_push_rate": cfg.Faults.DelayedPushRate,
			"duplicate_execution_rate": cfg.Faults.DuplicateExecutionRate, "ci_rerun_rate": cfg.Faults.CIRerunRate, "replay_delay_seconds": cfg.Faults.ReplayDelay.Seconds()},
	}
	if cfg.CI != nil {
		values["ci_workload"] = cfg.CI
	}
	if cfg.Lab != nil {
		values["lab"] = cfg.Lab
	}
	return json.Marshal(values)
}
