package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
)

type provenanceVector struct {
	Request Request `json:"request"`
	Events  []struct {
		Source  Source          `json:"source"`
		Payload json.RawMessage `json:"payload"`
	} `json:"events"`
}

func provenanceVectors(t *testing.T) map[string]provenanceVector {
	t.Helper()
	var vectors map[string]provenanceVector
	must(t, json.Unmarshal(read(t, "testdata/journal-v2.json"), &vectors))
	return vectors
}

func provenanceFixture(t *testing.T, v provenanceVector) *fixture {
	t.Helper()
	f := database(t)
	key := v.Events[0].Source.Queue
	_, err := f.admin.Exec(t.Context(), `INSERT INTO queues VALUES(1,$1,$2,$3,$4)`, key.Installation, key.Repository, key.Base, key.Lane)
	must(t, err)
	for _, event := range v.Events {
		_, err := f.admin.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload,recorded_at) VALUES($1,1,$2,$3,$4)`, event.Source.ID, event.Source.Type, event.Payload, event.Source.RecordedAt)
		must(t, err)
	}
	return f
}

func TestProvenanceV2IndependentVectors(t *testing.T) {
	for name, v := range provenanceVectors(t) {
		t.Run(name, func(t *testing.T) {
			f := provenanceFixture(t, v)
			out := filepath.Join(t.TempDir(), "export")
			m, err := f.reader.Export(t.Context(), v.Request, out)
			must(t, err)
			if m.Version != 2 {
				t.Fatal("provenance failed to select v2")
			}
			for _, file := range []string{"manifest.json", "events.jsonl", "selection.jsonl", "watermarks.jsonl", "dependency_boundaries.jsonl"} {
				if !bytes.Equal(read(t, filepath.Join(out, file)), read(t, filepath.Join("testdata", name+"-"+file))) {
					t.Fatalf("independent vector differs: %s", file)
				}
			}
			replay := filepath.Join(t.TempDir(), "replay")
			got, err := f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
			must(t, err)
			if got != m {
				t.Fatal("replay manifest changed")
			}
			manifests(t, out, replay)
			verifyAnchor(t, out, replay, m.ManifestSHA256, 0)
			refused := filepath.Join(t.TempDir(), "v1")
			if _, err := f.reader.ExportVersion(t.Context(), v.Request, refused, 1); err == nil {
				t.Fatal("explicit v1 lost provenance")
			}
			if _, err := os.Stat(refused); !os.IsNotExist(err) {
				t.Fatal("failed export published")
			}
		})
	}
}

func verifyAnchor(t *testing.T, original, replay, hash string, want int) {
	t.Helper()
	dir := t.TempDir()
	receipt, stdout := filepath.Join(dir, "receipt"), filepath.Join(dir, "stdout")
	must(t, os.WriteFile(receipt, []byte(hash+"\n"), 0600))
	var m Manifest
	must(t, json.Unmarshal(read(t, filepath.Join(replay, "manifest.json")), &m))
	must(t, os.WriteFile(stdout, []byte(replay+"\nmanifest SHA-256: "+m.ManifestSHA256+"\n"), 0600))
	output, err := exec.Command("python3", "../../scripts/verify-audit-anchor.py", "--anchor", receipt, "--replay-stdout", stdout, "--replay-out", replay, "--earlier", original).CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if code != want {
		t.Fatalf("anchor exit %d want %d: %s", code, want, output)
	}
}

func TestLegacyMainV1BytesAndReceipt(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	original := filepath.Join(t.TempDir(), "automatic")
	m, err := f.reader.Export(t.Context(), req(), original)
	must(t, err)
	if m.Version != 1 {
		t.Fatal("legacy export changed version")
	}
	manifests(t, "testdata/main-v1", original)
	replay := filepath.Join(t.TempDir(), "replay")
	_, err = f.reader.Replay(t.Context(), "testdata/main-v1/manifest.json", replay)
	must(t, err)
	manifests(t, original, replay)
	receipt := strings.TrimSpace(string(read(t, "testdata/main-v1/receipt")))
	verifyAnchor(t, original, replay, receipt, 0)
	explicit := filepath.Join(t.TempDir(), "explicit-v2")
	v2, err := f.reader.ExportVersion(t.Context(), req(), explicit, 2)
	must(t, err)
	if v2.Version != 2 {
		t.Fatal("explicit v2 ignored")
	}
	v2Replay := filepath.Join(t.TempDir(), "v2-replay")
	_, err = f.reader.Replay(t.Context(), filepath.Join(explicit, "manifest.json"), v2Replay)
	must(t, err)
	manifests(t, explicit, v2Replay)
	verifyAnchor(t, explicit, v2Replay, v2.ManifestSHA256, 0)
	// An old receipt cannot certify a new version, even for identical sources.
	verifyAnchor(t, explicit, v2Replay, receipt, 1)
	for _, version := range []int{-1, 3} {
		if _, err := f.reader.ExportVersion(t.Context(), req(), filepath.Join(t.TempDir(), "bad"), version); err != ErrSelectors {
			t.Fatal("unsupported export version accepted")
		}
	}
}

func TestProvenanceMissingAndInvalidEvidence(t *testing.T) {
	for _, name := range []string{"chunk", "decision", "layer", "wrong-kind", "decision-attempt", "decision-build", "inventory-digest", "record-digest", "layer-future", "duplicate-chunk", "unknown-field", "payload-kind", "foreign-queue", "missing-tested"} {
		t.Run(name, func(t *testing.T) {
			v := provenanceVectors(t)["v2-landing"]
			f := provenanceFixture(t, v)
			switch name {
			case "chunk":
				_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=163`)
				must(t, err)
			case "decision":
				_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=170`)
				must(t, err)
			case "layer":
				_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=160`)
				must(t, err)
			case "wrong-kind":
				_, err := f.admin.Exec(t.Context(), `UPDATE events SET type='historical' WHERE id=160`)
				must(t, err)
			case "duplicate-chunk":
				_, err := f.admin.Exec(t.Context(), `INSERT INTO events SELECT 162,queue_id,type,payload,recorded_at FROM events WHERE id=161`)
				must(t, err)
			case "payload-kind":
				_, err := f.admin.Exec(t.Context(), `UPDATE events SET type='change_provenance_observed' WHERE id=171`)
				must(t, err)
			case "foreign-queue":
				f.queue(t, 2, "foreign")
				_, err := f.admin.Exec(t.Context(), `UPDATE events SET queue_id=2 WHERE id=163`)
				must(t, err)
			case "missing-tested":
				_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=171; UPDATE events SET recorded_at='2026-10-22T12:00:00Z' WHERE id=201`)
				must(t, err)
			default:
				var landing pj.Landing
				for _, e := range v.Events {
					if e.Source.ID == 171 {
						ev, err := pj.DecodeEvent(e.Payload)
						must(t, err)
						landing = ev.(pj.Landing)
					}
				}
				switch name {
				case "decision-attempt":
					landing.AttemptID = "different"
				case "decision-build":
					landing.Members[0].Mapping.Build.Result = strings.Repeat("a", 40)
				case "inventory-digest":
					landing.Members[0].InventoryDigest = strings.Repeat("0", 64)
				case "record-digest":
					landing.Members[0].RecordDigest = strings.Repeat("0", 64)
				case "layer-future":
					landing.Members[0].Layers[0].References = []uint64{201}
				}
				if name != "record-digest" {
					landing.Members[0].RecordDigest = pj.MemberDigest(landing.Members[0])
				}
				raw, err := json.Marshal(map[string]any{"version": 1, "type": "landing_provenance_recorded", "data": landing})
				must(t, err)
				if name == "unknown-field" {
					raw = bytes.Replace(raw, []byte(`"stage":"tested"`), []byte(`"stage":"tested","future":true`), 1)
				}
				_, err = f.admin.Exec(t.Context(), `UPDATE events SET payload=$1 WHERE id=171`, raw)
				must(t, err)
			}
			out := filepath.Join(t.TempDir(), "failed")
			if _, err := f.reader.Export(t.Context(), v.Request, out); err == nil {
				t.Fatalf("accepted %s", name)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("invalid bundle published")
			}
		})
	}
}

func TestV2ReplayRejectsStrippedProvenance(t *testing.T) {
	v := provenanceVectors(t)["v2-decision"]
	f := provenanceFixture(t, v)
	out := filepath.Join(t.TempDir(), "export")
	_, err := f.reader.Export(t.Context(), v.Request, out)
	must(t, err)
	// Rehash every byte after removing one required generation chunk. Replay's
	// semantic closure, independently of the hash chain, must reject this.
	selected := readSelections(t, out)
	var retained []Selection
	for _, s := range selected {
		if s.Source.ID != 163 {
			retained = append(retained, s)
		}
	}
	forged := filepath.Join(t.TempDir(), "forged")
	must(t, os.Mkdir(forged, 0700))
	var manifest Manifest
	must(t, buildVersion(forged, v.Request, 2, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
		if err := water(watermark{QueueID: 1, Queue: v.Events[0].Source.Queue, ID: 201}); err != nil {
			return err
		}
		for _, s := range retained {
			for _, e := range v.Events {
				if e.Source.ID == s.Source.ID {
					if err := emit(sourceEvent{e.Source, e.Payload}, &s); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}, nil, &manifest))
	if _, err = f.reader.Replay(t.Context(), filepath.Join(forged, "manifest.json"), filepath.Join(t.TempDir(), "replay")); err == nil {
		t.Fatal("rehashing hid missing provenance")
	}
	// V1 manifests cannot carry v2 metadata. Reversion does not downgrade it.
	rewriteManifest(t, out, func(m *Manifest) { m.Version = 1 })
	if _, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), filepath.Join(t.TempDir(), "downgrade")); err == nil {
		t.Fatal("v2 silently downgraded")
	}
}

func TestProvenanceKindsRemainVersioned(t *testing.T) {
	for _, kind := range []string{"change_provenance_observed", "landing_provenance_recorded"} {
		if len(evidenceKinds(kind)) != 0 || fmt.Sprint(evidenceKindsVersion(kind, 2)) != "[provenance]" {
			t.Fatal("v1 vocabulary changed")
		}
	}
}

func TestV1ReplayDoesNotAcquireLaterProvenance(t *testing.T) {
	v := provenanceVectors(t)["v2-decision"]
	f := provenanceFixture(t, v)
	_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE type IN ('change_provenance_observed','landing_provenance_recorded')`)
	must(t, err)
	out := filepath.Join(t.TempDir(), "v1")
	m, err := f.reader.Export(t.Context(), v.Request, out)
	must(t, err)
	if m.Version != 1 {
		t.Fatal("legacy decision changed version")
	}
	for _, e := range v.Events {
		if isProvenance(e.Source.Type) {
			_, err = f.admin.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload,recorded_at) VALUES($1,1,$2,$3,$4)`, e.Source.ID, e.Source.Type, e.Payload, e.Source.RecordedAt)
			must(t, err)
		}
	}
	replay := filepath.Join(t.TempDir(), "replay")
	_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
	must(t, err)
	manifests(t, out, replay)
	fresh := filepath.Join(t.TempDir(), "fresh")
	m, err = f.reader.Export(t.Context(), v.Request, fresh)
	must(t, err)
	if m.Version != 2 {
		t.Fatal("fresh decision closure missed provenance")
	}
}

func TestV2EmptyExport(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	out := filepath.Join(t.TempDir(), "empty")
	m, err := f.reader.ExportVersion(t.Context(), req(), out, 2)
	must(t, err)
	if m.Version != 2 || m.Count != 0 || m.TerminalSHA256 != zeroDigest {
		t.Fatal("empty v2 contract changed")
	}
	replay := filepath.Join(t.TempDir(), "replay")
	_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
	must(t, err)
	manifests(t, out, replay)
}

func TestInvalidCompanionStageCannotDowngradeDecision(t *testing.T) {
	v := provenanceVectors(t)["v2-decision"]
	f := provenanceFixture(t, v)
	_, err := f.admin.Exec(t.Context(), `DELETE FROM events WHERE id=201; UPDATE events SET payload=jsonb_set(payload,'{data,stage}','"future"') WHERE id=171`)
	must(t, err)
	if _, err := f.reader.Export(t.Context(), v.Request, filepath.Join(t.TempDir(), "bad")); err == nil {
		t.Fatal("invalid out-of-range companion silently downgraded decision")
	}
}
