package sim

import (
	"fmt"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestSpeculativeChainPlanner(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PRs = 40
			cfg.ArrivalWindow = time.Hour
			cfg.CIDuration = 10 * time.Minute
			cfg.MergeMethod = method
			cfg.BrokenRate = .1
			cfg.FlakeRate = .05
			cfg.CI = &CIConfig{Runners: 50, RerunsSupported: true}
			cfg.Planner.SpeculationMaxDepth = 4
			cfg.Planner.BatchMaxSize = 4
			cfg.Planner.BatchMaxWait = time.Minute
			s, r := runScenario(t, cfg)
			if r.Merged+sumDequeued(r) != cfg.PRs {
				t.Fatal("lost changes")
			}
			starts := 0
			for _, e := range s.events {
				if _, ok := e.Event.(queue.ChainedBatchStarted); ok {
					starts++
				}
			}
			if starts < 4 {
				t.Fatal("chain not exercised")
			}
		})
	}
}

func TestSpeculativeFastForwardIndependentHeads(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 8
	cfg.ArrivalWindow = 0
	cfg.Faults = Faults{}
	cfg.MergeMethod = queue.FastForward
	cfg.Planner.SpeculationMaxDepth = 4
	_, result := runScenario(t, cfg)
	if result.Merged != 1 || result.Dequeued[queue.FastForwardRequired] != 7 {
		t.Fatalf("fast-forward speculation changed ancestry policy: %+v", result)
	}
}

func TestProvisionalConflictInSimulator(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 3
	cfg.ArrivalWindow = 0
	cfg.Faults = Faults{}
	cfg.CI = &CIConfig{Runners: 50, RerunsSupported: true}
	cfg.Planner.SpeculationMaxDepth = 4
	cfg.CIDuration = 10 * time.Minute
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	injected := false
	for range 100 {
		actions, err := s.plan()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range actions {
			if build, ok := a.(queue.BuildChainedTestRef); ok && build.ParentBatchID != "" {
				s.forge.FailNext("BuildTestRef", forge.ErrConflict)
				injected = true
				break
			}
		}
		_, err = s.step()
		if err != nil {
			t.Fatalf("simulator rejected mutation: %v", err)
		}
		if injected {
			break
		}
	}
	if !injected {
		t.Fatal("did not inject provisional conflict")
	}
	if s.violation != nil {
		t.Fatalf("simulator rejected mutation: %v", s.violation)
	}
	if _, err := s.run(); err != nil {
		t.Fatalf("simulator rejected mutation: %v", err)
	}
}

func TestSpeculativeChainRecoversFaults(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			cfg := smallConfig()
			cfg.Seed = seed
			cfg.PRs = 24
			cfg.ArrivalWindow = 0
			cfg.Faults = DefaultFaults()
			cfg.CI = &CIConfig{Runners: 8, RerunsSupported: true}
			cfg.Planner.SpeculationMaxDepth = 4
			cfg.Planner.BatchMaxSize = 4
			cfg.DropWebhookRate = .3
			cfg.ExternalPushRate = .1
			cfg.RepushRate = .1
			cfg.BrokenRate = .1
			cfg.FlakeRate = .05
			s, err := newSimulation(cfg)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.run()
			if err != nil {
				t.Fatalf("simulation recovery: %v", err)
			}
			if r.StillQueued != 0 || r.FaultsRecovered != r.FaultsInjected {
				t.Fatalf("chain recovery lost work: %+v; pending faults=%v", r, s.pendingFaults)
			}
		})
	}
}

func TestSpeculationEnablesRerunFaultCapability(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 0
	cfg.Faults = Faults{Methods: map[string]float64{"RerunFailedChecks": 0}}
	cfg.Planner.SpeculationMaxDepth = 4
	if _, err := Run(cfg); err != nil {
		t.Fatalf("implicit speculative retry capability rejected: %v", err)
	}
}
