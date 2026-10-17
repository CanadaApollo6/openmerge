package sim

import (
	"github.com/CanadaApollo6/openmerge/internal/sim/capacity"
	"testing"
)

// This non-gating experiment can be rerun with the documented admission overlay
// to compare speculative work behind a known provisional failure. The pinned
// 30-day exit tests remain separate and unchanged.
func TestAdmissionPolicyEvidence(t *testing.T) {
	for _, p := range []capacity.Profile{capacity.Team(), capacity.Slow(), capacity.AdaptiveWorkload()} {
		for _, cancel := range []bool{true, false} {
			var samples []capacity.Sample
			runs, wasted := 0, 0
			minutes := 0.
			for seed := 1; seed <= 8; seed++ {
				cfg := plannerProfile(p, uint64(seed), cancel, 1)
				cfg.Planner.SpeculationMaxDepth = 4
				if p.Adaptive {
					cfg.Planner.BatchTargetPassRate = .8
				}
				_, r := runScenario(t, cfg)
				for _, m := range r.CI.Merges {
					samples = append(samples, capacity.Sample{Change: m.PR, Arrival: m.Enqueued, TTM: m.TimeToMerge})
				}
				runs += r.CIRuns
				wasted += r.CI.WastedRuns
				minutes += r.CI.RunnerMinutes
			}
			t.Logf("ADMISSION %s cancellation=%t: %+v; wasted=%d", p.Name, cancel, capacity.Summarize(samples, runs, minutes, p.Threshold), wasted)
		}
	}
}
