package store

import (
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/journalreader"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Regression probes supplied by Astra for PR #188.
func TestRecoveryRejectsContradictoryBindings(t *testing.T) {
	for _, scenario := range []string{"wrong-tested-decision", "superseded-generation", "wrong-landed-result"} {
		t.Run(scenario, func(t *testing.T) {
			s := newStore(t)
			q, claim, c, d := decisionLand(t, s)
			must(t, startWithSide(t.Context(), s, claim, c, d, landingSide(t, c)))
			var wrongDecision int64
			if scenario == "wrong-tested-decision" {
				wrong := d
				wrong.TestedSHA = strings.Repeat("f", 40)
				wrongDecision = appendEvents(t, s, q, wrong)
			}
			landedID := appendEvents(t, s, q, queue.Landed{BatchID: c.Start.BatchID, NewBaseSHA: d.LandedSHA, NewFence: "next"})
			var raw []byte
			must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='landing_provenance_recorded'`, q).Scan(&raw))
			e, err := pj.DecodeEvent(raw)
			must(t, err)
			landed := e.(pj.Landing)
			landed.Stage = "landed"
			landed.Members[0].Mapping.Build.Stage = "landed"
			landed.Members[0].Mapping.Build.ID = "recovered-landed"
			landed.Members[0].Mapping.Build.Result = d.LandedSHA
			if scenario == "wrong-tested-decision" {
				landed.DecisionID = uint64(wrongDecision)
				for i := range landed.Members[0].Layers {
					landed.Members[0].Layers[i].References = []uint64{uint64(wrongDecision)}
				}
			}
			if scenario == "superseded-generation" {
				must(t, s.pool.QueryRow(t.Context(), `SELECT payload FROM events WHERE queue_id=$1 AND type='change_provenance_observed'`, q).Scan(&raw))
				obsEvent, err := pj.DecodeEvent(raw)
				must(t, err)
				obs := obsEvent.(pj.Observation)
				obs.Request.Generation = "replacement-generation"
				obs.PreviousObservationID = landed.Members[0].ObservationIDs[0]
				obs.Digest = pj.InventoryDigest(obs, obs.Commits)
				newID, err := s.AppendSide(t.Context(), q, sideObservation(obs, "replacement-generation"))
				must(t, err)
				_ = newID // Keep the old generation in the recovery request.
			}
			if scenario == "wrong-landed-result" {
				landed.Members[0].Mapping.Build.Result = strings.Repeat("9", 40)
			}
			landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
			request := SideAppend{Count: 1, Build: func([]uint64) ([]journalreader.Event, error) { return []journalreader.Event{{Provenance: landed}}, nil }}
			_, err = s.RecoverLanded(t.Context(), q, landedID, request)
			if err == nil {
				t.Fatalf("recovery accepted %s", scenario)
			}
			t.Logf("rejected: %v", err)
		})
	}
}
