package audit

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestProvenanceBatchIntermediateExportAndSecondaryDecisionClosure(t *testing.T) {
	for _, secondaryOnly := range []bool{false, true} {
		name := "batch"
		if secondaryOnly {
			name = "secondary-decision-only"
		}
		t.Run(name, func(t *testing.T) {
			v := provenanceVectors(t)["v2-decision"]
			secondID, err := queue.PRChangeID("7", "Owner/Repo", "release", "", 13)
			must(t, err)
			intermediate := strings.Repeat("7", 40)
			var observations []pj.Observation
			var decision queue.LandingDecisionRecorded
			var extra []struct {
				Source  Source          `json:"source"`
				Payload json.RawMessage `json:"payload"`
			}
			for i, event := range v.Events {
				if event.Source.Type == "landing_decision_recorded" {
					e, err := queue.DecodeEvent(event.Payload)
					must(t, err)
					decision = e.(queue.LandingDecisionRecorded)
				}
				if event.Source.Type == "change_revision_observed" {
					e, err := queue.DecodeEvent(event.Payload)
					must(t, err)
					revision := e.(queue.ChangeRevisionObserved)
					revision.PR = 13
					revision.ChangeID = secondID
					_, raw, err := queue.EncodeEvent(revision)
					must(t, err)
					cloned := event
					cloned.Source.ID = 21
					cloned.Payload = raw
					extra = append(extra, cloned)
				}
				if event.Source.Type == "batch_started" {
					e, err := queue.DecodeEvent(event.Payload)
					must(t, err)
					b := e.(queue.BatchStarted)
					b.PRs = append(b.PRs, queue.BatchPR{PR: 13, HeadSHA: b.PRs[0].HeadSHA})
					_, raw, err := queue.EncodeEvent(b)
					must(t, err)
					v.Events[i].Payload = raw
				}
				if event.Source.Type == "landing_checks_recorded" {
					e, err := queue.DecodeEvent(event.Payload)
					must(t, err)
					c := e.(queue.LandingChecksRecorded)
					m := c.Members[0]
					m.PR = 13
					c.Members = append(c.Members, m)
					_, raw, err := queue.EncodeEvent(c)
					must(t, err)
					v.Events[i].Payload = raw
				}
				if event.Source.Type == "change_provenance_observed" {
					e, err := pj.DecodeEvent(event.Payload)
					must(t, err)
					h := e.(pj.Observation)
					h.Request.ChangeID = string(secondID)
					h.Request.BuildBase = intermediate
					observations = append(observations, h)
				}
			}
			commits := []p.Commit{}
			for _, h := range observations {
				commits = append(commits, h.Commits...)
			}
			digest := pj.InventoryDigest(observations[0], commits)
			for i, h := range observations {
				h.Digest = digest
				_, raw, err := pj.EncodeEvent(h)
				must(t, err)
				event := v.Events[0]
				event.Source.ID = int64(164 + i)
				event.Source.Type = "change_provenance_observed"
				event.Payload = raw
				extra = append(extra, event)
			}
			second := queue.LandingDecisionRecorded{BatchID: decision.BatchID, AttemptID: decision.AttemptID, TestedSHA: decision.TestedSHA, LandedSHA: decision.LandedSHA, Variant: "generated_remedy", Remedy: &queue.GeneratedRemedyDecision{ChangeID: secondID, Revision: 1, Head: decision.Approval.Basis.Head, Suspect: decision.Approval.Basis.Head, Evidence: []string{"fixture"}, OnRed: "revert"}}
			_, raw, err := queue.EncodeEvent(second)
			must(t, err)
			event := v.Events[0]
			event.Source.ID = 169
			event.Source.Type = "landing_decision_recorded"
			event.Source.RecordedAt = "2026-10-22T12:00:00.000000Z"
			event.Payload = raw
			extra = append(extra, event)
			for i, event := range v.Events {
				if event.Source.Type != "landing_provenance_recorded" {
					continue
				}
				e, err := pj.DecodeEvent(event.Payload)
				must(t, err)
				l := e.(pj.Landing)
				secondMember := l.Members[0]
				secondMember.Request = observations[0].Request
				secondMember.ObservationIDs = []uint64{164, 165}
				secondMember.InventoryDigest = digest
				secondMember.Mapping.Request = secondMember.Request
				secondMember.Mapping.Build.Base = intermediate
				secondMember.Layers = append([]pj.Layer{}, secondMember.Layers...)
				for j := range secondMember.Layers {
					secondMember.Layers[j].References = []uint64{169}
				}
				secondMember.RecordDigest = pj.MemberDigest(secondMember)
				l.Members[0].Mapping.Build.Result = intermediate
				l.Members[0].Mapping.Commits = append([]p.CommitMapping{}, l.Members[0].Mapping.Commits...)
				for j := range l.Members[0].Mapping.Commits {
					l.Members[0].Mapping.Commits[j].Build = intermediate
				}
				l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
				l.Members = append(l.Members, secondMember)
				_, raw, err := pj.EncodeEvent(l)
				must(t, err)
				v.Events[i].Payload = raw
			}
			v.Events = append(v.Events, extra...)
			f := provenanceFixture(t, v)
			if secondaryOnly {
				_, err := f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T11:00:00Z' WHERE type='landing_decision_recorded' AND id<>169`)
				must(t, err)
			}
			out := filepath.Join(t.TempDir(), "export")
			m, err := f.reader.Export(t.Context(), v.Request, out)
			must(t, err)
			if m.Version != 2 {
				t.Fatal("secondary decision lost provenance")
			}
			selected := readSelections(t, out)
			found := map[int64]bool{}
			for _, s := range selected {
				found[s.Source.ID] = true
			}
			for _, id := range []int64{161, 163, 164, 165, 169, 170, 171, 201} {
				if !found[id] {
					t.Fatalf("batch closure omitted %d", id)
				}
			}
			_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			must(t, err)
		})
	}
}
