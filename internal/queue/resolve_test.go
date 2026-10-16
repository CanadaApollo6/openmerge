package queue

import (
	"testing"
	"time"
)

func TestResolveLandingPinsAttemptInputs(t *testing.T) {
	s := landingState()
	// Recovery must use the attempt's fence even if another observation changed
	// the state's current fence before the timed-out attempt is resolved.
	s.Fence = "later-observation"
	actions := Plan(testConfig, s, epoch.Add(2*time.Minute))
	if len(actions) != 1 {
		t.Fatalf("Plan = %v, want resolution", actions)
	}
	a, ok := actions[0].(ResolveLanding)
	if !ok || a.BatchID != "b1" || a.AttemptID != "attempt1" || a.TestedSHA != "tested1" || a.FenceToken != "f0" {
		t.Fatalf("resolution lost durable attempt inputs: %+v", actions[0])
	}
}
