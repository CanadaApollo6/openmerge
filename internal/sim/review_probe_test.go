package sim

import (
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/sim/capacity"
)

func TestReviewCancellationActualSavings(t *testing.T) {
	for _, p := range []capacity.Profile{capacity.Team(), capacity.Slow()} {
		requested, cancelled, queued := 0, 0, 0
		var saved time.Duration
		for seed := 1; seed <= 8; seed++ {
			cfg := plannerProfile(p, uint64(seed), true, 30)
			cfg.Planner.SpeculationMaxDepth = p.Depth
			s, _ := runScenario(t, cfg)
			for _, job := range s.ciJobs {
				if job.cancellationRequested {
					requested++
				}
				if job.cancelled {
					cancelled++
					if job.started.IsZero() {
						queued++
					} else {
						saved += job.duration - job.finished.Sub(job.started)
					}
				}
			}
		}
		t.Logf("%s requested=%d cancelled=%d cancelled-before-start=%d confirmed-already-completed=%d saved-runner-minutes=%.8f", p.Name, requested, cancelled, queued, requested-cancelled, saved.Minutes())
	}
}
