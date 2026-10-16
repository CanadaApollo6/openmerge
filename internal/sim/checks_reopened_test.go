package sim

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
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

func TestDelayedPassCannotReviveLandingInSimulator(t *testing.T) {
	s := onePR(t)
	land := readyLanding(t, s)
	s.executor.NewID = func() (string, error) {
		// Preflight read success; a reopen and then a delayed old pass commit.
		s.forge.SetCheckRuns(s.repo, land.TestedSHA, []forge.CheckRun{{Name: "ci", State: forge.Pending}})
		s.record(queue.ChecksReopened{TestedSHA: land.TestedSHA, At: s.now})
		s.record(queue.ChecksCompleted{TestedSHA: land.TestedSHA, Success: true})
		return "delayed-pass", nil
	}
	err := s.executor.Execute(t.Context(), land, func(e queue.Event) error { return s.emit(land, e) })
	if !errors.Is(err, errStale) || s.state.Active == nil || s.state.Active.Phase != queue.Testing || s.metrics().Merged != 0 {
		t.Fatalf("simulator rejected mutation: delayed pass revived invalidated Land: err=%v state=%+v", err, s.state.Active)
	}
}
