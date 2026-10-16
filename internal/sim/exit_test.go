package sim

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestExit500PRDay(t *testing.T) {
	for _, size := range []int{1, 8} {
		for _, variant := range []struct {
			name               string
			faults             bool
			drop               float64
			delayed, duplicate float64
			method             queue.MergeMethod
		}{
			{name: "baseline"},
			{name: "faults", faults: true},
			{name: "dropped-webhooks", drop: 0.3},
			{name: "faults-and-dropped-webhooks", faults: true, drop: 0.3},
			{name: "delayed-pushes", delayed: 0.1},
			{name: "duplicate-execution", duplicate: 0.1},
			{name: "faults-squash", faults: true, method: queue.Squash},
			{name: "faults-rebase", faults: true, method: queue.Rebase},
		} {
			t.Run(fmt.Sprintf("batch-%d/%s", size, variant.name), func(t *testing.T) {
				cfg := DefaultConfig()
				cfg.Seed = 42
				cfg.Planner.BatchMaxSize = size
				if size > 1 {
					cfg.Planner.BatchMaxWait = 30 * time.Second
				}
				cfg.DropWebhookRate = variant.drop
				if variant.faults {
					cfg.Faults = DefaultFaults()
					cfg.ExternalPushRate = 0.02
				}
				if variant.delayed > 0 {
					cfg.Faults.DelayedPushRate = variant.delayed
				}
				if variant.duplicate > 0 {
					cfg.Faults.DuplicateExecutionRate = variant.duplicate
				}
				cfg.MergeMethod = variant.method
				s, result := runScenario(t, cfg)
				if result.Merged != cfg.PRs-result.Broken || result.Dequeued[queue.ChecksFailed] != result.Broken || result.StillQueued != 0 {
					t.Fatalf("day did not drain all passing PRs: %+v", result)
				}
				if len(result.Dequeued) != 1 || result.Blocked[queue.Untrusted] != result.Untrusted {
					t.Fatalf("unexpected outcomes: %+v", result)
				}
				for pr := 1; pr < len(s.prs); pr++ {
					p := s.prs[pr]
					switch {
					case p.untrusted:
						if !p.landed.IsZero() || p.dequeued != "" {
							t.Fatalf("untrusted PR %d admitted: %+v", pr, p)
						}
					case p.broken:
						if !p.landed.IsZero() || p.dequeued != queue.ChecksFailed {
							t.Fatalf("broken PR %d outcome: %+v", pr, p)
						}
					default:
						if p.landed.IsZero() || p.dequeued != "" {
							t.Fatalf("passing PR %d outcome: %+v", pr, p)
						}
					}
				}
				if result.SimulatedSpan > 25*time.Hour {
					t.Fatalf("two-minute serial CI failed to keep up: %s", result.SimulatedSpan)
				}
				if variant.faults && (result.FaultsInjected == 0 || result.FaultsRecovered != result.FaultsInjected) {
					t.Fatalf("faults did not recover: %+v", result)
				}
				if variant.faults {
					if result.ExternalPushes == 0 {
						t.Fatal("external main pushes not exercised")
					}
					for _, method := range []string{"EnsureFence", "BuildTestRef", "CheckRuns", "ReportStatus", "DeleteRef", "Land", "ReadFence", "AdvanceFence"} {
						if result.FaultsByMethod[method] == 0 {
							t.Fatalf("%s fault not exercised", method)
						}
					}
					if variant.method == queue.Squash || variant.method == queue.Rebase {
						if result.FaultsByMethod["MarkLanded"] == 0 {
							t.Fatal("MarkLanded fault not exercised")
						}
					}
				}
				if variant.delayed > 0 && (result.DelayedPushes == 0 || result.RejectedReplays != result.DelayedPushes) {
					t.Fatalf("late abandoned pushes not rejected: %+v", result)
				}
				if variant.duplicate > 0 && result.DuplicateExecutions == 0 {
					t.Fatal("duplicate executions not exercised")
				}
				if result.FaultsRecovered != result.FaultsInjected {
					t.Fatalf("unrecovered fault: %+v", result)
				}
				if variant.drop > 0 && result.DroppedWebhooks == 0 {
					t.Fatal("no webhooks dropped")
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				t.Log(string(data))
			})
		}
	}

}

func runScenario(t *testing.T, cfg Config) (*simulation, Result) {
	t.Helper()
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	initial := s.state
	result, err := s.run()
	if err != nil {
		t.Fatalf("%v; result=%+v", err, result)
	}
	// The in-memory log is the durable boundary: replay must rebuild the same
	// projection, including acknowledged cleanup and recovered landing attempts.
	replayed := initial
	for _, entry := range s.events {
		replayed = queue.Apply(replayed, entry.Event)
	}
	if !reflect.DeepEqual(replayed, s.state) {
		t.Fatal("event log did not reproduce the final projection")
	}
	return s, result
}

func TestExit2000PRDayBatch8(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Seed = 42
	cfg.PRs = 2000
	cfg.CIDuration = 30 * time.Minute
	cfg.Planner.CheckTimeout = time.Hour
	cfg.Planner.BatchMaxSize = 8
	cfg.Planner.BatchMaxWait = 30 * time.Second
	_, result := runScenario(t, cfg)
	if result.Merged != cfg.PRs-result.Broken || result.StillQueued != 0 || result.Dequeued[queue.ChecksFailed] != result.Broken {
		t.Fatalf("lost passing PRs: %+v", result)
	}
	if result.BisectionCIRuns == 0 {
		t.Fatal("no bisection CI exercised")
	}
	// Seed 42 baseline: 0.40535 runs/merge and 161h56m52s median. Leave
	// approximately 10% headroom while catching throughput/latency regressions.
	if result.CIRunsPerMergedPR > 0.446 || result.MedianTimeToMerge > 179*time.Hour {
		t.Fatalf("batch-8 performance regressed: runs/merge=%.5f (max 0.446), median=%s (max 179h)", result.CIRunsPerMergedPR, result.MedianTimeToMerge)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}

func TestBatch8HeadChangesAndConflicts(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs = 80
	cfg.Planner.BatchMaxSize = 8
	cfg.RepushRate = 0.25
	cfg.ConflictRate = 0.25
	cfg.Faults = DefaultFaults()
	_, result := runScenario(t, cfg)
	if result.Merged == 0 || result.Dequeued[queue.HeadChanged] == 0 || result.Dequeued[queue.Conflict] == 0 {
		t.Fatalf("batch churn was not exercised: %+v", result)
	}
}
