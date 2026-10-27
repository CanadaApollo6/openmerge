package queue

import (
	"reflect"
	"testing"
)

func TestNilEventDoesNotRunQueueMaintenance(t *testing.T) {
	// A side-evidence row supplies no queue event. Even stale derived evidence
	// must survive this call unchanged: only a real queue event may run maintenance.
	s := State{landingDecisions: map[string]LandingDecisionRecorded{
		"stale": {BatchID: "settled", AttemptID: "old"},
	}}
	got := Apply(s, nil)
	if !reflect.DeepEqual(got, s) {
		t.Fatal("nil side event ran queue maintenance")
	}
}
