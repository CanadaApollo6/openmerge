package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
)

func TestReviewR2InterveningGeneration(t *testing.T) {
	v := provenanceVectors(t)["v2-observation"]
	f := provenanceFixture(t, v)
	obs := observationForScale(t)
	obs.Request.Generation = "intervening-generation"
	obs.PreviousObservationID = 161
	obs.Digest = pj.InventoryDigest(obs, obs.Commits)
	typ, raw, err := pj.EncodeEvent(obs)
	must(t, err)
	_, err = f.admin.Exec(t.Context(), `INSERT INTO events VALUES(162,1,$1,$2,'2026-10-22T11:00:00Z')`, typ, raw)
	must(t, err)
	var state pj.State
	for _, id := range []int64{161, 162, 163} {
		var raw []byte
		must(t, f.admin.QueryRow(t.Context(), `SELECT payload::text FROM events WHERE id=$1`, id).Scan(&raw))
		e, err := pj.DecodeEvent(raw)
		must(t, err)
		next, err := pj.Apply(state, uint64(id), e)
		t.Logf("raw fold id=%d error=%v", id, err)
		if id == 163 && !errors.Is(err, pj.ErrBinding) {
			t.Fatalf("raw fold must reject stale chunk: %v", err)
		}
		if id != 163 {
			must(t, err)
		}
		state = next
	}
	for _, version := range []int{0, 2} {
		parent := t.TempDir()
		out := filepath.Join(parent, "export")
		m, err := f.reader.ExportVersion(t.Context(), v.Request, out, version)
		t.Logf("version=%d export count=%d error=%v", version, m.Count, err)
		if err == nil {
			_, re := f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
			t.Logf("replay error=%v", re)
			t.Fatal("stale chunk silently exported despite intervening generation")
		}
		if !errors.Is(err, errSource) {
			t.Fatalf("expected source binding rejection: %v", err)
		}
		entries, err := os.ReadDir(parent)
		must(t, err)
		if len(entries) != 0 {
			t.Fatal("rejected stale chunk left staging or output")
		}
	}
}

// Duplicate identities leave an empty, terminal marker even after a third row.
// An ambiguous unrelated identity must not poison otherwise valid lookups.
func TestProvenanceDuplicateMarker(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		for _, copies := range []int{2, 3} {
			t.Run(fmt.Sprintf("unrelated=%t/copies=%d", unrelated, copies), func(t *testing.T) {
				v := provenanceVectors(t)["v2-observation"]
				f := provenanceFixture(t, v)
				obs := observationForScale(t)
				if unrelated {
					obs.Request.ChangeID = "unrelated"
				} else {
					// Duplicate the selected generation's nonzero chunk identity.
					for _, event := range v.Events {
						if event.Source.ID == 163 {
							e, err := pj.DecodeEvent(event.Payload)
							must(t, err)
							obs = e.(pj.Observation)
						}
					}
				}
				if unrelated {
					obs.Digest = pj.InventoryDigest(obs, obs.Commits)
				}
				typ, raw, err := pj.EncodeEvent(obs)
				must(t, err)
				n := copies - 1
				if unrelated {
					n = copies
				}
				for i := 0; i < n; i++ {
					_, err = f.admin.Exec(t.Context(), `INSERT INTO events VALUES($1,1,$2,$3,'2026-10-22T14:00:00Z')`, 211+i, typ, raw)
					must(t, err)
				}
				parent := t.TempDir()
				out := filepath.Join(parent, "export")
				m, err := f.reader.Export(t.Context(), v.Request, out)
				if unrelated {
					must(t, err)
					if m.Count != 2 {
						t.Fatalf("unrelated duplicates entered closure: %d", m.Count)
					}
					_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "replay"))
					must(t, err)
				} else {
					if !errors.Is(err, errSource) {
						t.Fatalf("ambiguous lookup accepted: %v", err)
					}
					entries, err := os.ReadDir(parent)
					must(t, err)
					if len(entries) != 0 {
						t.Fatal("failed export left staging or output")
					}
				}
			})
		}
	}
}

func TestProvenanceScratchLimits(t *testing.T) {
	v := provenanceVectors(t)["v2-observation"]
	f := provenanceFixture(t, v)
	good := filepath.Join(t.TempDir(), "export")
	_, err := f.reader.Export(t.Context(), v.Request, good)
	must(t, err)
	for _, tc := range []struct {
		name     string
		limits   ScratchLimits
		resource string
	}{
		{"index-bytes", ScratchLimits{MaxBytes: 4096}, "bytes"},
		{"index-files", ScratchLimits{MaxFiles: 2}, "files"},
		{"fold-files", ScratchLimits{MaxFiles: 6}, "files"},
		{"fold-bytes", ScratchLimits{MaxBytes: 6 * 4096}, "bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.reader.ScratchLimits = tc.limits
			for _, replay := range []bool{false, true} {
				parent := t.TempDir()
				out := filepath.Join(parent, "export")
				if replay {
					_, err = f.reader.Replay(t.Context(), filepath.Join(good, "manifest.json"), out)
				} else {
					_, err = f.reader.Export(t.Context(), v.Request, out)
				}
				var limit *ScratchLimitError
				if !errors.Is(err, ErrScratchLimit) || !errors.As(err, &limit) || limit.Resource != tc.resource {
					t.Fatalf("replay=%t: expected typed %s limit, got %v", replay, tc.resource, err)
				}
				entries, err := os.ReadDir(parent)
				must(t, err)
				if len(entries) != 0 {
					t.Fatal("scratch limit left staging or output")
				}
			}
		})
	}
}

// Exercise the production audit closure adapter above the live projection's
// 4096-record ceiling. Small end-to-end exports assert SQL work separately;
// repeating their canonical bundle serialization thousands of times adds no
// retention/linearity coverage and risks the snapshot deadline under -race.
func TestProvenanceLargeClosure(t *testing.T) {
	const n = 4097
	obs := emptyScaleObservation(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "selected")
	source := provenanceVectors(t)["v2-observation"].Events[0].Source
	source.Type = "change_provenance_observed"
	must(t, spoolFile(path, func(out io.Writer) error {
		for i := 1; i <= n; i++ {
			obs.Request.ChangeID = fmt.Sprint(i)
			obs.Digest = pj.InventoryDigest(obs, obs.Commits)
			raw, err := json.Marshal(struct {
				Version int            `json:"version"`
				Type    string         `json:"type"`
				Data    pj.Observation `json:"data"`
			}{pj.WireVersion, source.Type, obs})
			if err != nil {
				return err
			}
			source.ID = int64(i)
			if err := spoolWrite(out, spoolRecord{Source: source, Payload: raw}); err != nil {
				return err
			}
		}
		return nil
	}))
	budget, err := newScratchBudget(ScratchLimits{})
	must(t, err)
	must(t, validateProvenanceClosure(path, dir, budget))
	// Each independent inventory stores exactly one group and one latest key;
	// the shared fold directory is the only additional budgeted entry.
	if budget.files != 2*n+1 {
		t.Fatalf("closure storage growth: %d entries for %d records", budget.files, n)
	}
	t.Logf("selected=%d scratch_entries=%d scratch_bytes=%d", n, budget.files, budget.bytes)
}
