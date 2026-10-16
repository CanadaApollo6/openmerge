// Package sim runs the serial queue and executor against a fake forge using
// only a seeded random source and a discrete virtual clock.
package sim

import (
	"fmt"
	"math"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Config describes trusted PRs plus one additional untrusted fork probe per 50
// PRs (at least one for a nonempty workload). Arrivals have uniform jitter in
// ArrivalWindow; enqueue follows observation by one simulated second.
type Config struct {
	PRs             int
	Seed            uint64
	ArrivalWindow   time.Duration
	BrokenRate      float64
	FlakeRate       float64
	DropWebhookRate float64
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

// Faults injects independent pre-call infrastructure errors and crashes after a
// successful landing push but before its Landed event is durably recorded.
type Faults struct {
	ForgeErrorRate   float64
	LandingCrashRate float64
}

// DefaultFaults exercises transient reads, writes, and uncertain landings.
func DefaultFaults() Faults {
	return Faults{ForgeErrorRate: 0.02, LandingCrashRate: 0.02}
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

// Result contains aggregate outcomes. All JSON durations are nanoseconds.
// Untrusted probes are extra PRs, counted separately from PRs and Broken.
type Result struct {
	PRs               int                         `json:"prs"`
	Broken            int                         `json:"broken"`
	Untrusted         int                         `json:"untrusted"`
	Merged            int                         `json:"merged"`
	Dequeued          map[queue.DequeueReason]int `json:"dequeued"`
	Blocked           map[queue.DequeueReason]int `json:"blocked"`
	StillQueued       int                         `json:"still_queued"`
	MeanTimeToMerge   time.Duration               `json:"mean_time_to_merge_ns"`
	P95TimeToMerge    time.Duration               `json:"p95_time_to_merge_ns"`
	CIRuns            int                         `json:"ci_runs"`
	CIRunsPerMergedPR float64                     `json:"ci_runs_per_merged_pr"`
	SimulatedSpan     time.Duration               `json:"simulated_span_ns"`
	Steps             int                         `json:"steps"`
	FaultsInjected    int                         `json:"faults_injected"`
	FaultsRecovered   int                         `json:"faults_recovered"`
	DroppedWebhooks   int                         `json:"dropped_webhooks"`
}

func (cfg Config) normalized() (Config, error) {
	if cfg.PRs < 0 || cfg.ArrivalWindow < 0 {
		return cfg, fmt.Errorf("PR count and arrival window must be nonnegative")
	}
	rates := []struct {
		name string
		rate float64
	}{
		{"broken rate", cfg.BrokenRate}, {"flake rate", cfg.FlakeRate},
		{"drop webhook rate", cfg.DropWebhookRate},
		{"forge error rate", cfg.Faults.ForgeErrorRate}, {"landing crash rate", cfg.Faults.LandingCrashRate},
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
	case queue.Merge, queue.Squash, queue.Rebase:
	default:
		return cfg, fmt.Errorf("unsupported merge method %q", cfg.MergeMethod)
	}
	cfg.Planner.MergeMethod = cfg.MergeMethod
	if cfg.RetryDelay < 0 || cfg.MaxSteps < 0 {
		return cfg, fmt.Errorf("retry delay and step limit must be nonnegative")
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = time.Second
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 1_000_000
	}
	return cfg, nil
}
