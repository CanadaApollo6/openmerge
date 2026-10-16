package sim

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCIRerunAfterSuccess(t *testing.T) {
	cfg := smallConfig()
	cfg.Faults = Faults{CIRerunRate: 1}
	s, err := newSimulation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.run()
	if err != nil {
		t.Fatalf("simulator rejected CI rerun: %v; result=%+v", err, r)
	}
	if r.CIReruns != cfg.PRs || r.Merged != cfg.PRs || r.FaultsInjected != r.FaultsRecovered {
		t.Fatalf("reruns did not recover: %+v", r)
	}
	reopened := 0
	for _, e := range s.events {
		if _, ok := e.Event.(queue.ChecksReopened); ok {
			reopened++
		}
	}
	if reopened < r.CIReruns {
		t.Fatalf("reruns did not revoke success: reopened=%d reruns=%d", reopened, r.CIReruns)
	}
}
