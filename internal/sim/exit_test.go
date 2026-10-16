package sim

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestExit500PRDay(t *testing.T) {
	for _, variant := range []struct {
		name   string
		faults bool
		drop   float64
	}{
		{name: "baseline"},
		{name: "faults", faults: true},
		{name: "dropped-webhooks", drop: 0.3},
		{name: "faults-and-dropped-webhooks", faults: true, drop: 0.3},
	} {
		t.Run(variant.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Seed = 42
			cfg.DropWebhookRate = variant.drop
			if variant.faults {
				cfg.Faults = DefaultFaults()
			}
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
