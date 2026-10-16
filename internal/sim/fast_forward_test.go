package sim

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFastForwardSimulatorDequeuesIndependentHeads(t *testing.T) {
	for _, batchSize := range []int{1, 8} {
		cfg := smallConfig()
		cfg.MergeMethod = queue.FastForward
		cfg.PRs = 8
		cfg.ArrivalWindow = 0
		cfg.Planner.BatchMaxSize = batchSize
		s, result := runScenario(t, cfg)
		if result.Merged != 1 || result.Dequeued[queue.FastForwardRequired] != 7 || result.StillQueued != 0 {
			t.Fatalf("wrong fast-forward outcome: %+v", result)
		}
		for tip, run := range s.tests {
			if len(run.heads) != 1 || tip != run.heads[0].HeadSHA {
				t.Fatal("simulator rewrote head or combined siblings")
			}
		}
	}
}

func TestFastForwardSimulatorRecoversLandingFaults(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 1
	cfg.MergeMethod = queue.FastForward
	cfg.Faults = DefaultFaults()
	cfg.Faults.LandingCrashRate = 1
	_, result := runScenario(t, cfg)
	if result.Merged != 1 || result.FaultsInjected == 0 || result.FaultsRecovered != result.FaultsInjected {
		t.Fatalf("fast-forward recovery failed: %+v", result)
	}
}
