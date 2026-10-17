package capacity

import "time"

// Profile is shared by the report and the planner-backed exit harness. The
// latter must explicitly acquire planner chain support before asserting a gate.
type Profile struct {
	Name       string
	PerDay     int
	CI         time.Duration
	Depth      int
	BrokenRate float64
	Adaptive   bool
	Threshold  time.Duration
}

// Team is the ADR 0009 team adopter workload.
func Team() Profile {
	return Profile{Name: "Team", PerDay: 20, CI: 30 * time.Minute, Depth: 4, BrokenRate: .02, Threshold: time.Hour}
}

// Slow is the ADR 0009 slow-CI adopter workload.
func Slow() Profile {
	return Profile{Name: "Slow", PerDay: 5, CI: 3 * time.Hour, Depth: 4, BrokenRate: .02, Threshold: 270 * time.Minute}
}

// AdaptiveWorkload is pinned after evaluating candidate loads; see the report.
func AdaptiveWorkload() Profile {
	return Profile{Name: "Adaptive", PerDay: 900, CI: 2 * time.Minute, Depth: 4, BrokenRate: .05, Adaptive: true, Threshold: 8 * time.Minute}
}

// Config expands a profile into its pinned 30-day seeded workload.
func (p Profile) Config(seed uint64, cancellation bool) Config {
	return Config{Seed: seed, Changes: p.PerDay * 30, ArrivalWindow: 30 * 24 * time.Hour, CI: p.CI, MaxWait: 5 * time.Minute, CheckTimeout: p.CI * 3 / 2, BrokenRate: p.BrokenRate, FlakeRate: .01, MaxSize: 8, MinDepth: 1, MaxDepth: p.Depth, Runners: 50, Cancellation: cancellation, Retries: p.Depth > 1, Adaptive: p.Adaptive, Target: .8}
}
