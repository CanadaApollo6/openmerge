package audit

import (
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestEvidenceAuthorityRequiredAtDecisionBoundary(t *testing.T) {
	for _, mode := range []string{"github-stage-a-dogfood", "mixed-activation", "rollback", "required-check-missing"} {
		t.Run(mode, func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			f := fx.f
			// Stage A approval history is present while no authority/check instrumentation
			// governed the original decision: this matches the offline dogfood shape.
			_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=$1 OR id=$2`, fx.pinID, fx.checksID)
			must(t, err)
			expected := map[int64]bool{fx.decisionID: false}
			if mode != "github-stage-a-dogfood" {
				pin := fx.events["policy_authority_applied"][0].(queue.PolicyAuthorityApplied)
				evidenceInsert(t, f, pin)
				d := fx.decision
				d.AttemptID = "active"
				active := evidenceInsert(t, f, d)
				expected[active] = true
				c := fx.events["landing_checks_recorded"][0].(queue.LandingChecksRecorded)
				c.AttemptID = d.AttemptID
				if mode != "required-check-missing" {
					evidenceInsert(t, f, c)
				}
				start := fx.events["landing_started"][0].(queue.LandingStarted)
				start.AttemptID = d.AttemptID
				evidenceInsert(t, f, start)
				if mode == "rollback" {
					pin.Authority = "github"
					pin.OpenMerge = nil
					evidenceInsert(t, f, pin)
					d.AttemptID = "rolled-back"
					id := evidenceInsert(t, f, d)
					expected[id] = false
					start.AttemptID = d.AttemptID
					evidenceInsert(t, f, start)
				}
			}
			out := filepath.Join(t.TempDir(), "export")
			_, err = f.reader.Export(t.Context(), Request{Installation: "7", Repository: "Owner/Repo"}, out)
			if mode == "required-check-missing" {
				if err != errSource {
					t.Fatalf("required check absence got %v", err)
				}
				return
			}
			must(t, err)
			seen := map[int64]bool{}
			for _, s := range readSelections(t, out) {
				required, ok := expected[s.Source.ID]
				if !ok {
					continue
				}
				seen[s.Source.ID] = true
				for _, typ := range []string{"policy_authority_applied", "landing_checks_recorded"} {
					found := false
					for _, ref := range s.References {
						if ref.ExpectedType != typ {
							continue
						}
						found = true
						if ref.Owner.EventID != s.Source.ID || ref.Owner.BatchID != fx.decision.BatchID {
							t.Fatal("wrong absence owner", ref)
						}
						if required {
							if ref.Status != "recorded" || ref.Source == nil {
								t.Fatal("governing evidence absent", ref)
							}
						} else if ref.Status != "not_recorded" || ref.Source != nil {
							t.Fatal("inapplicable evidence acquired source IDs", ref)
						}
					}
					if !found {
						t.Fatal("missing expected kind", typ)
					}
				}
			}
			if len(seen) != len(expected) {
				t.Fatal("decision scope incomplete", seen, expected)
			}
			replay := filepath.Join(t.TempDir(), "replay")
			_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
			must(t, err)
			manifests(t, out, replay)
		})
	}
}
