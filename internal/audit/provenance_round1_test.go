package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// Astra's out-of-range companion repro, including alternate numeric spellings
// and malformed shapes. No requested format may turn broken evidence into absence.
func TestProvenanceMalformedCompanionID(t *testing.T) {
	for _, id := range []string{"170.0", "1.700e2", "\"170\"", "null", "170.5"} {
		t.Run(id, func(t *testing.T) {
			v := provenanceVectors(t)["v2-decision"]
			f := provenanceFixture(t, v)
			_, err := f.admin.Exec(t.Context(), `UPDATE events SET payload=jsonb_set(payload,'{data,decision_id}',$1::jsonb) WHERE type='landing_provenance_recorded'`, id)
			must(t, err)
			for _, version := range []int{0, 1, 2} {
				out := filepath.Join(t.TempDir(), "export")
				if _, err := f.reader.ExportVersion(t.Context(), v.Request, out, version); err == nil {
					t.Fatalf("malformed companion accepted: version=%d", version)
				}
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("failed export published")
				}
			}
		})
	}
}

func observationForScale(t *testing.T) pj.Observation {
	t.Helper()
	v := provenanceVectors(t)["v2-observation"]
	var obs pj.Observation
	for _, e := range v.Events {
		if e.Source.ID == 161 {
			ev, err := pj.DecodeEvent(e.Payload)
			must(t, err)
			obs = ev.(pj.Observation)
		}
	}
	for _, e := range v.Events {
		if e.Source.ID == 163 {
			ev, err := pj.DecodeEvent(e.Payload)
			must(t, err)
			obs.Commits = append(obs.Commits, ev.(pj.Observation).Commits...)
		}
	}
	obs.Chunks = 1
	return obs
}

func provenanceScaleFixture(t *testing.T, n int, all bool) (*fixture, Request) {
	t.Helper()
	return provenanceScaleFixtureObservation(t, n, all, observationForScale(t))
}

func provenanceScaleFixtureObservation(t *testing.T, n int, all bool, obs pj.Observation) (*fixture, Request) {
	t.Helper()
	v := provenanceVectors(t)["v2-observation"]
	f := database(t)
	q := v.Events[0].Source.Queue
	_, err := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES(1,$1,$2,$3,$4)`, q.Installation, q.Repository, q.Base, q.Lane)
	must(t, err)
	rows := make([][]any, 0, n)
	for i := 1; i <= n; i++ {
		obs.Request.ChangeID = fmt.Sprintf("change-%d", i)
		obs.Digest = pj.InventoryDigest(obs, obs.Commits)
		typ, raw, err := pj.EncodeEvent(obs)
		must(t, err)
		at := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
		if all || i == n {
			at = at.Add(time.Hour)
		}
		rows = append(rows, []any{i, 1, typ, string(raw), at})
	}
	_, err = f.admin.CopyFrom(t.Context(), pgx.Identifier{"events"}, []string{"id", "queue_id", "type", "payload", "recorded_at"}, pgx.CopyFromRows(rows))
	must(t, err)
	return f, v.Request
}

func emptyScaleObservation(t *testing.T) pj.Observation {
	t.Helper()
	// Complete inventories can be empty when bases/exclusions remove all
	// originals. Nonempty chunks and declarations have separate regressions.
	obs := observationForScale(t)
	obs.Commits = []p.Commit{}
	obs.Stable, obs.Additional = []string{}, []string{}
	var err error
	obs.DeclarationDigest, err = p.DeclarationDigest(obs.Commits, true, nil)
	must(t, err)
	return obs
}

func TestProvenanceLinearExport(t *testing.T) {
	obs := emptyScaleObservation(t)
	for _, n := range []int{32, 96} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f, req := provenanceScaleFixtureObservation(t, n, true, obs)
			tr := &queryTrace{}
			r := traced(t, f, tr)
			start := time.Now()
			m, err := r.Export(t.Context(), req, filepath.Join(t.TempDir(), "export"))
			must(t, err)
			if m.Count != int64(n) || m.Version != 2 {
				t.Fatalf("wrong selection: %+v", m)
			}
			indexes, prefixes := 0, 0
			for _, sql := range tr.queries {
				if sql == provenanceIndexSQL {
					indexes++
				}
				if strings.Contains(sql, "type IN ('change_provenance_observed','landing_provenance_recorded')") && sql != provenanceIndexSQL {
					prefixes++
				}
			}
			if indexes != 1 || prefixes != 0 {
				t.Fatalf("repeated provenance scans: indexes=%d prefixes=%d", indexes, prefixes)
			}
			// Independent observations require no DB evidence lookups. Only the scope
			// pages grow; fold storage operation counts are tested in journal itself.
			if len(tr.queries) > n/pageSize+12 {
				t.Fatalf("query growth: %d for %d records", len(tr.queries), n)
			}
			t.Logf("history=%d selected=%d queries=%d index_scans=%d export=%s", n, m.Count, len(tr.queries), indexes, time.Since(start))
		})
	}
}

func TestProvenanceHistoricalRetentionLimit(t *testing.T) {
	f, req := provenanceScaleFixture(t, 5000, false)
	// These unrelated, strictly decodable inventories deliberately have bad
	// assembled digests. A whole-history fold would reject them; closure must
	// validate only the one independent selected inventory.
	_, err := f.admin.Exec(t.Context(), `UPDATE events SET payload=jsonb_set(payload,'{data,digest}',to_jsonb(repeat('0',64))) WHERE id<5000`)
	must(t, err)
	out := filepath.Join(t.TempDir(), "export")
	start := time.Now()
	m, err := f.reader.Export(t.Context(), req, out)
	must(t, err)
	if m.Count != 1 {
		t.Fatalf("unrelated history entered closure: %d", m.Count)
	}
	_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
	must(t, err)
	t.Logf("history=5000 selected=1 export_and_replay=%s", time.Since(start))
}

func TestProvenancePreviousGenerationClosure(t *testing.T) {
	v := provenanceVectors(t)["v2-observation"]
	f := provenanceFixture(t, v)
	_, err := f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T11:00:00Z'`)
	must(t, err)
	obs := observationForScale(t)
	obs.Request.Generation = "generation-2"
	obs.PreviousObservationID = 161
	obs.Digest = pj.InventoryDigest(obs, obs.Commits)
	all := obs.Commits
	for i, id := range []int64{211, 213} {
		obs.Chunks = 2
		obs.Index = i
		obs.Commits = all[i : i+1]
		typ, raw, err := pj.EncodeEvent(obs)
		must(t, err)
		at := "2026-10-22T12:00:00Z"
		if i == 1 {
			at = "2026-10-22T14:00:00Z"
		}
		_, err = f.admin.Exec(t.Context(), `INSERT INTO events VALUES($1,1,$2,$3,$4)`, id, typ, raw, at)
		must(t, err)
	}
	out := filepath.Join(t.TempDir(), "export")
	m, err := f.reader.Export(t.Context(), v.Request, out)
	must(t, err)
	if m.Count != 4 {
		t.Fatalf("previous generation lost: count=%d", m.Count)
	}
	got := readSelections(t, out)
	for i, id := range []int64{161, 163, 211, 213} {
		if got[i].Source.ID != id {
			t.Fatalf("missing generation source %d", id)
		}
	}
	_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
	must(t, err)
	// This fixture has only queues/events, with no side projection available.
	// Cross-generation export and replay therefore cannot depend on retained
	// records or on how the composed reader prunes/compacts them.

}

func TestProvenanceDecisionAttemptMismatchIsolated(t *testing.T) {
	v := provenanceVectors(t)["v2-decision"]
	f := provenanceFixture(t, v)
	for _, event := range v.Events {
		if event.Source.Type != "landing_provenance_recorded" {
			continue
		}
		e, err := pj.DecodeEvent(event.Payload)
		must(t, err)
		l := e.(pj.Landing)
		l.AttemptID = "different-attempt"
		_, raw, err := pj.EncodeEvent(l)
		must(t, err)
		_, err = f.admin.Exec(t.Context(), `UPDATE events SET payload=$1 WHERE id=$2`, raw, event.Source.ID)
		must(t, err)
	}
	// Both stages agree, all member digests and IDs remain valid. Only the
	// binding to the unchanged legacy decision's attempt is inconsistent.
	if _, err := f.reader.Export(t.Context(), v.Request, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("decision attempt mismatch accepted")
	}
}

func TestProvenanceDecisionMemberMismatchIsolated(t *testing.T) {
	v := provenanceVectors(t)["v2-decision"]
	f := provenanceFixture(t, v)
	obs := observationForScale(t)
	obs.Request.ChangeID = "other-member"
	obs.Digest = pj.InventoryDigest(obs, obs.Commits)
	for _, event := range v.Events {
		if !isProvenance(event.Source.Type) {
			continue
		}
		e, err := pj.DecodeEvent(event.Payload)
		must(t, err)
		switch value := e.(type) {
		case pj.Observation:
			value.Request = obs.Request
			value.Digest = obs.Digest
			e = value
		case pj.Landing:
			value.Members[0].Request = obs.Request
			value.Members[0].Mapping.Request = obs.Request
			value.Members[0].InventoryDigest = obs.Digest
			value.Members[0].RecordDigest = pj.MemberDigest(value.Members[0])
			e = value
		}
		_, raw, err := pj.EncodeEvent(e)
		must(t, err)
		_, err = f.admin.Exec(t.Context(), `UPDATE events SET payload=$1 WHERE id=$2`, raw, event.Source.ID)
		must(t, err)
	}
	if _, err := f.reader.Export(t.Context(), v.Request, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("unrelated member accepted for decision")
	}
}

// Compare automatic legacy closure to the unchanged v1 resolver over a large
// queue. Ignore only the single provenance inventory scan: all other SQL must
// match the v1 resolver's multiset, regardless of the number of decisions.
func TestProvenanceLegacyCost(t *testing.T) {
	for _, n := range []int{10, 30} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fx := round3ApprovalFixture(t)
			f := fx.f
			for i := 1; i < n; i++ {
				d := fx.decision
				d.AttemptID = fmt.Sprintf("cost-%d", i)
				evidenceInsert(t, f, d)
				c := fx.events["landing_checks_recorded"][0].(queue.LandingChecksRecorded)
				c.AttemptID = d.AttemptID
				evidenceInsert(t, f, c)
				start := fx.events["landing_started"][0].(queue.LandingStarted)
				start.AttemptID = d.AttemptID
				evidenceInsert(t, f, start)
			}
			_, err := f.admin.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload,recorded_at) SELECT 1,'history','{}','2026-10-22T11:00:00Z' FROM generate_series(1,300000)`)
			must(t, err)
			req := Request{Installation: "7", Repository: "Owner/Repo", From: "2026-10-22T12:00:00Z", Until: "2026-10-22T13:00:00Z"}
			// Fixtures put their decisions in the scope at 12:00.
			tr := &queryTrace{}
			r := traced(t, f, tr)
			out := filepath.Join(t.TempDir(), "export")
			start := time.Now()
			m, err := r.Export(t.Context(), req, out)
			must(t, err)
			elapsed := time.Since(start)
			if m.Version != 1 {
				t.Fatal("legacy version changed")
			}
			counts := map[string]int{}
			index := 0
			for _, sql := range tr.queries {
				if sql == provenanceIndexSQL {
					index++
				} else {
					counts[sql]++
				}
			}
			if index != 1 {
				t.Fatalf("legacy provenance queries=%d", index)
			}
			tr.mu.Lock()
			tr.queries = nil
			tr.mu.Unlock()
			start = time.Now()
			_, err = r.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			must(t, err)
			replay := time.Since(start)
			// Replay has different source fetching/manifest queries, so compare the
			// evidence lookups (the unchanged v1 semantic resolver) specifically.
			v1 := map[string]int{}
			for _, sql := range tr.queries {
				if strings.HasPrefix(sql, "WITH retained_events AS NOT MATERIALIZED") {
					v1[sql]++
				}
			}
			for sql, n := range counts {
				if strings.HasPrefix(sql, "WITH retained_events AS NOT MATERIALIZED") && v1[sql] != n {
					t.Fatalf("legacy lookup cost changed: export=%d replay=%d", n, v1[sql])
				}
			}
			t.Logf("queue_history=300000 decisions=%d selected=%d automatic_export=%s v1_replay=%s provenance_scans=%d", n, m.Count, elapsed, replay, index)
		})
	}
}

func TestProvenanceRawGenerationFences(t *testing.T) {
	for _, bad := range []string{"missing-previous", "stale-member"} {
		t.Run(bad, func(t *testing.T) {
			v := provenanceVectors(t)["v2-decision"]
			f := provenanceFixture(t, v)
			obs := observationForScale(t)
			obs.Request.Generation = "intervening-generation"
			if bad == "missing-previous" {
				// A new observation claims there was no prior generation. Its omitted
				// predecessor must still be found in the immutable journal.
				_, err := f.admin.Exec(t.Context(), `UPDATE events SET recorded_at='2026-10-22T11:00:00Z'`)
				must(t, err)
			} else {
				obs.PreviousObservationID = 161
			}
			obs.Digest = pj.InventoryDigest(obs, obs.Commits)
			typ, raw, err := pj.EncodeEvent(obs)
			must(t, err)
			id := int64(169)
			at := "2026-10-22T11:00:00Z"
			if bad == "missing-previous" {
				id = 211
				at = "2026-10-22T12:00:00Z"
			}
			_, err = f.admin.Exec(t.Context(), `INSERT INTO events VALUES($1,1,$2,$3,$4)`, id, typ, raw, at)
			must(t, err)
			if _, err := f.reader.Export(t.Context(), v.Request, filepath.Join(t.TempDir(), "bad")); err == nil {
				t.Fatal("raw generation fence lost", bad)
			}
		})
	}
}
