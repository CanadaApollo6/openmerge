package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV1DependencyAndHistoricalAbsenceReplay(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	m, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	var event envelope
	must(t, json.Unmarshal(read(t, filepath.Join(original, "events.jsonl")), &event))
	var selected Selection
	must(t, json.Unmarshal(read(t, filepath.Join(original, "selection.jsonl")), &selected))
	selected.Role = "dependency"
	selected.References = []Reference{
		{Source: selected.Source, Relationship: "requires", Status: "recorded"},
		{Source: Source{QueueID: 1, ID: 99, Queue: selected.Source.Queue}, Relationship: "caused_by", Status: "not_recorded"},
	}
	meta, e := metadata(selected)
	must(t, e)
	selected.MetadataSHA256 = digest(meta)
	complete := t.TempDir()
	must(t, build(complete, m.Request, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
		if e := water(watermark{QueueID: 1, Queue: selected.Source.Queue, ID: selected.Source.ID}); e != nil {
			return e
		}
		return emit(sourceEvent{source: selected.Source, payload: event.Payload}, &selected)
	}, nil, &m, func(out *stream) error {
		for _, ref := range selected.References {
			raw, e := encode(ref)
			if e != nil {
				return e
			}
			if e = out.write(raw); e != nil {
				return e
			}
		}
		return nil
	}))
	replay := filepath.Join(t.TempDir(), "replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(complete, "manifest.json"), replay)
	must(t, e)
	manifests(t, complete, replay)
	if string(read(t, filepath.Join(complete, "dependency_boundaries.jsonl"))) != string(read(t, filepath.Join(replay, "dependency_boundaries.jsonl"))) {
		t.Fatal("boundary replay changed")
	}
	for _, file := range []string{"selection.jsonl", "dependency_boundaries.jsonl"} {
		t.Run(file, func(t *testing.T) {
			corrupt := copyExport(t, complete)
			// copyExport covers all v1 streams; explicitly include boundaries for old helpers too.
			must(t, os.WriteFile(filepath.Join(corrupt, "dependency_boundaries.jsonl"), read(t, filepath.Join(complete, "dependency_boundaries.jsonl")), 0600))
			raw := strings.Replace(string(read(t, filepath.Join(corrupt, file))), `"relationship":"requires"`, `"relationship":"requires","future":true`, 1)
			must(t, os.WriteFile(filepath.Join(corrupt, file), []byte(raw), 0600))
			rewriteManifest(t, corrupt, func(m *Manifest) {
				if file == "selection.jsonl" {
					m.SelectionSHA256 = digest([]byte(raw))
				} else {
					m.DependencyBoundariesSHA256 = digest([]byte(raw))
				}
			})
			if _, e := f.reader.Replay(t.Context(), filepath.Join(corrupt, "manifest.json"), filepath.Join(t.TempDir(), "bad")); e == nil {
				t.Fatal("accepted unknown v1 reference field")
			}
		})
	}
}

func TestReplayRejectsWatermarkIDFromAnotherQueue(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	f.queue(t, 2, "other")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	foreignID := insert(t, f.admin, 2, "2026-10-22T14:00:00Z")
	insert(t, f.admin, 1, "2026-10-22T14:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	corrupt := copyExport(t, original)
	waters := readWatermarks(t, corrupt)
	waters[0].ID = foreignID
	writeWatermarks(t, corrupt, waters)
	if _, e = f.reader.Replay(t.Context(), filepath.Join(corrupt, "manifest.json"), filepath.Join(t.TempDir(), "bad")); e == nil {
		t.Fatal("accepted watermark ID absent from its queue")
	}
}
