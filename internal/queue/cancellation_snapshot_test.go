package queue

import (
	"encoding/json"
	"testing"
)

func TestCancellationSnapshotRejectsMalformedObservationsAndDuplicateEffects(t *testing.T) {
	cfg, s, at := chainState(t, 4, 1)
	s, b := admitChain(t, cfg, s, at)
	run := CheckRun{ID: 1, Attempt: 1, Name: "ci", Source: "app:1", State: "pending"}
	s = Apply(s, BatchRunsObserved{BatchID: b.ID, TestedSHA: b.TestedSHA, Runs: []CheckRun{run}})
	for _, mode := range []string{"duplicate producer", "zero ID", "bogus state", "duplicate effect", "invalid last observation"} {
		t.Run(mode, func(t *testing.T) {
			data, err := EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			var wire stateSnapshot
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "duplicate producer":
				wire.Chain[0].Batch.ObservedRuns = append(wire.Chain[0].Batch.ObservedRuns, run)
			case "zero ID":
				wire.Chain[0].Batch.ObservedRuns[0].ID = 0
			case "bogus state":
				wire.Chain[0].Batch.ObservedRuns[0].State = "bogus"
			case "invalid last observation":
				wire.Chain[0].Batch.LastObservedRuns[0].ID = 0
			case "duplicate effect":
				_, raw, err := EncodeAction(CancelChecks{BatchID: "retired", TestedSHA: "sha", TestRef: "ref", Reason: "closed", Runs: []CheckRun{run}})
				if err != nil {
					t.Fatal(err)
				}
				wire.Effects = []json.RawMessage{raw, raw}
			}
			data, err = json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(data); err == nil {
				t.Fatal("accepted malformed snapshot")
			}
		})
	}
}
