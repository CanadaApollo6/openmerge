package sim

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func smallConfig() Config {
	cfg := DefaultConfig()
	cfg.PRs, cfg.Seed, cfg.BrokenRate, cfg.ArrivalWindow = 12, 42, 0, 0
	return cfg
}

func TestScenarios(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*Config)
		reason queue.DequeueReason
	}{
		{name: "all pass", change: func(*Config) {}},
		{name: "flakes dequeue good PRs", change: func(c *Config) { c.FlakeRate = 1 }, reason: queue.ChecksFailed},
		{name: "broken PRs fail", change: func(c *Config) { c.BrokenRate = 1 }, reason: queue.ChecksFailed},
		{name: "all webhooks lost", change: func(c *Config) { c.DropWebhookRate = 1 }},
		{name: "timeouts reconcile before dequeue", change: func(c *Config) {
			c.Planner.CheckTimeout = 30 * time.Second
			c.Planner.RefreshInterval = time.Hour
			c.DropWebhookRate = 1
		}, reason: queue.ChecksTimeout},
		{name: "completion at timeout wins", change: func(c *Config) {
			c.Planner.CheckTimeout = c.CIDuration
			c.DropWebhookRate = 1
		}},
		{name: "timeout read discovers missed pass", change: func(c *Config) {
			c.Planner.CheckTimeout = 3 * time.Minute
			c.Planner.RefreshInterval = time.Hour
			c.DropWebhookRate = 1
		}},
		{name: "every landing crashes", change: func(c *Config) { c.Faults.LandingCrashRate = 1 }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg := smallConfig()
			scenario.change(&cfg)
			s, result := runScenario(t, cfg)
			if scenario.reason == "" {
				if result.Merged != cfg.PRs || len(result.Dequeued) != 0 {
					t.Fatalf("passing PRs lost: %+v", result)
				}
			} else if result.Merged != 0 || result.Dequeued[scenario.reason] != cfg.PRs || result.CIRuns != cfg.PRs {
				t.Fatalf("incorrect terminal failures: %+v", result)
			}
			if result.StillQueued != 0 || result.Blocked[queue.Untrusted] != result.Untrusted {
				t.Fatalf("queue did not drain or trust gate failed: %+v", result)
			}
			if cfg.Faults.LandingCrashRate == 1 && (result.FaultsInjected != cfg.PRs || result.FaultsRecovered != cfg.PRs) {
				t.Fatalf("crashed pushes not recovered through ResolveLanding: %+v", result)
			}
			for _, run := range s.tests {
				for _, head := range run.heads {
					if s.prs[head.PR].untrusted {
						t.Fatalf("untrusted fork PR %d built", head.PR)
					}
				}
			}
		})
	}
}

func TestMergeMethods(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			cfg := smallConfig()
			cfg.MergeMethod, cfg.Faults, cfg.DropWebhookRate = method, DefaultFaults(), 0.3
			s, result := runScenario(t, cfg)
			if result.Merged != cfg.PRs {
				t.Fatalf("not all PRs merged: %+v", result)
			}
			for pr := 1; pr <= cfg.PRs; pr++ {
				actual, err := s.forge.PullRequest(context.Background(), s.repo, pr)
				if err != nil || !actual.Closed {
					t.Fatalf("PR %d not closed: %+v, %v", pr, actual, err)
				}
				if method != queue.Merge && len(s.forge.Comments(s.repo, pr)) != 1 {
					t.Fatalf("PR %d missing idempotent landing link", pr)
				}
			}
		})
	}
}

func TestDeterminism(t *testing.T) {
	cfg := smallConfig()
	cfg.CIMin, cfg.CIMax = time.Minute, 3*time.Minute
	cfg.BrokenRate, cfg.FlakeRate, cfg.DropWebhookRate, cfg.Faults = 0.1, 0.2, 0.3, DefaultFaults()
	a, first := runScenario(t, cfg)
	b, second := runScenario(t, cfg)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(a.events, b.events) {
		t.Fatalf("same seed changed result or event log:\n%+v\n%+v", first, second)
	}
	built := make(map[string]time.Time)
	for _, entry := range a.events {
		switch event := entry.Event.(type) {
		case queue.TestRefBuilt:
			built[event.TestedSHA] = event.At
		case queue.ChecksCompleted:
			elapsed := entry.At.Sub(built[event.TestedSHA])
			// Reconciliation may see a terminal result again at a later time.
			if start, ok := built[event.TestedSHA]; ok {
				if elapsed < cfg.CIMin || elapsed > cfg.CIMax+cfg.Planner.RefreshInterval {
					t.Fatalf("CI started %s, completed after %s outside configured range", start, elapsed)
				}
				delete(built, event.TestedSHA)
			}
		}
	}
}

func TestFailedCheckReadRetriedAfterDelay(t *testing.T) {
	cfg := smallConfig()
	cfg.PRs, cfg.RetryDelay = 1, 10*time.Second
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.forge.FailNext("CheckRuns", errForge)
	result, err := s.run()
	if err != nil || result.Merged != 1 {
		t.Fatalf("read failure lost PR: %+v, %v", result, err)
	}
	for _, entry := range s.events {
		if event, ok := entry.Event.(queue.ChecksRefreshed); ok {
			if elapsed := event.At.Sub(s.start); elapsed != time.Second+cfg.RetryDelay {
				t.Fatalf("first successful retry at %s, want %s", elapsed, time.Second+cfg.RetryDelay)
			}
			return
		}
	}
	t.Fatal("failed read never retried")
}

func TestWedgesFailLoudly(t *testing.T) {
	for _, scenario := range []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "step limit", cfg: Config{PRs: 1, MaxSteps: 2}, want: "wedge: exceeded 2 steps"},
		{name: "paused build", cfg: Config{PRs: 1, Faults: Faults{Methods: map[string]float64{"BuildTestRef": 1}}}, want: "wedge:"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := Run(scenario.cfg)
			if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("error=%v, want %q", err, scenario.want)
			}
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, cfg := range []Config{
		{PRs: -1}, {ArrivalWindow: -1}, {BrokenRate: -0.1}, {FlakeRate: 1.1},
		{DropWebhookRate: math.NaN()}, {Faults: Faults{ForgeErrorRate: math.Inf(1)}},
		{Faults: Faults{LandingCrashRate: -1}}, {CIDuration: -1},
		{CIMin: time.Second}, {CIMax: time.Second}, {CIMin: time.Minute, CIMax: time.Second},
		{RepushRate: -1}, {ConflictRate: math.NaN()},
		{Faults: Faults{DelayedPushRate: 2}}, {Faults: Faults{DuplicateExecutionRate: -1}},
		{Faults: Faults{Methods: map[string]float64{"AdvanceFence": 2}}},
		{Faults: Faults{Methods: map[string]float64{"Unknown": 0.1}}},
		{Faults: Faults{FailFirst: map[string]int{"Land": -1}}},
		{Faults: Faults{FailFirst: map[string]int{"Unknown": 1}}},
		{Faults: Faults{ReplayDelay: -1}},
		{MergeMethod: "fast-forward"}, {RetryDelay: -1}, {MaxSteps: -1},
	} {
		if _, err := Run(cfg); err == nil {
			t.Errorf("accepted invalid config %+v", cfg)
		}
	}
}

func TestInvariantRejectsUnsafeHistory(t *testing.T) {
	for _, scenario := range []struct {
		name string
		want string
	}{
		{name: "pending CI", want: "no passing completed CI"},
		{name: "failed CI", want: "no passing completed CI"},
		{name: "broken PR", want: "contains broken PR 1"},
		{name: "head changed", want: "differs from tested head"},
		{name: "duplicate PR", want: "landed twice"},
		{name: "untrusted fork", want: "untrusted PR 2"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg := smallConfig()
			cfg.PRs = 1
			if scenario.name == "broken PR" {
				cfg.BrokenRate = 1
			}
			s, err := newSimulation(cfg)
			if err != nil {
				t.Fatal(err)
			}
			number := 1
			if scenario.name == "untrusted fork" {
				number = 2
			}
			if err := s.deliver(scheduledEvent{kind: arrival, pr: number}); err != nil {
				t.Fatal(err)
			}
			unsafePush := func(batch string) string {
				t.Helper()
				sha, err := s.forge.BuildTestRef(context.Background(), s.repo, forge.BuildRequest{
					Ref: "openmerge/queue/main/" + batch, BaseSHA: s.mainSHA, BatchID: batch, Method: forge.Merge,
					PRs: []forge.BuildPR{{Number: number, HeadSHA: s.prs[number].head}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.forge.SetRef(s.repo, "main", sha); err != nil {
					t.Fatal(err)
				}
				return sha
			}
			sha := unsafePush("bad")
			run := s.tests[sha]
			run.done = scenario.name != "pending CI"
			run.success = scenario.name != "failed CI"
			if scenario.name == "head changed" {
				if _, err := s.forge.PushPR(s.repo, number, "new head after testing"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.name == "duplicate PR" {
				if err := s.checkInvariants(); err != nil {
					t.Fatal(err)
				}
				sha = unsafePush("duplicate")
				s.tests[sha].done, s.tests[sha].success = true, true
			}
			if err := s.checkInvariants(); err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("unsafe history error=%v, want %q", err, scenario.want)
			}
		})
	}
}
