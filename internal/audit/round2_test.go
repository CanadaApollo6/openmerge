package audit

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSourceReplayAfterNewQueue(t *testing.T) {
	for _, withEvents := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "events"}[withEvents], func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
			original := filepath.Join(t.TempDir(), "original")
			_, e := f.reader.Export(t.Context(), req(), original)
			must(t, e)
			f.queue(t, 2, "new")
			if withEvents {
				insert(t, f.admin, 2, "2026-10-22T12:00:00Z")
			}
			replay := filepath.Join(t.TempDir(), "replay")
			_, e = f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), replay)
			must(t, e)
			manifests(t, original, replay)
		})
	}
}

func TestSourceReplayAllowsUnselectedQueueOmission(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	f.queue(t, 2, "empty")
	insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
	original := filepath.Join(t.TempDir(), "original")
	_, e := f.reader.Export(t.Context(), req(), original)
	must(t, e)
	waters := readWatermarks(t, original)
	writeWatermarks(t, original, waters[:1])
	replay := filepath.Join(t.TempDir(), "replay")
	_, e = f.reader.Replay(t.Context(), filepath.Join(original, "manifest.json"), replay)
	must(t, e)
	manifests(t, original, replay)
}

// Build internally coherent artifacts, including all metadata, stream and chain
// hashes. Rejection must come from semantic validation rather than stale hashes.
func TestSourceReplayRejectsScopeShapeAndCrossQueueOrder(t *testing.T) {
	for _, name := range []string{"bad-role", "dependency-role", "null-references", "nonempty-references", "cross-queue-order"} {
		t.Run(name, func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			f.queue(t, 2, "z")
			insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
			insert(t, f.admin, 2, "2026-10-22T12:00:00Z")
			original := filepath.Join(t.TempDir(), "original")
			m, e := f.reader.Export(t.Context(), req(), original)
			must(t, e)
			selections := readSelections(t, original)
			waters := readWatermarks(t, original)
			var events []envelope
			must(t, lines(bytes.NewReader(read(t, filepath.Join(original, "events.jsonl"))), func(raw []byte) error {
				var v envelope
				if e := json.Unmarshal(raw, &v); e != nil {
					return e
				}
				events = append(events, v)
				return nil
			}))
			switch name {
			case "bad-role":
				selections[0].Role = "invalid"
			case "dependency-role":
				selections[0].Role = "dependency"
			case "null-references":
				selections[0].References = nil
			case "nonempty-references":
				selections[0].References = []Reference{{}}
			case "cross-queue-order":
				selections[0], selections[1] = selections[1], selections[0]
				events[0], events[1] = events[1], events[0]
			}
			for i := range selections {
				raw, e := metadata(selections[i])
				must(t, e)
				selections[i].MetadataSHA256 = digest(raw)
			}
			coherent := t.TempDir()
			must(t, build(coherent, m.Request, func(emit func(sourceEvent, *Selection) error, water func(watermark) error) error {
				for _, w := range waters {
					if e := water(w); e != nil {
						return e
					}
				}
				for i, s := range selections {
					if e := emit(sourceEvent{source: s.Source, payload: events[i].Payload}, &s); e != nil {
						return e
					}
				}
				return nil
			}, nil, &m))
			if _, e = f.reader.Replay(t.Context(), filepath.Join(coherent, "manifest.json"), filepath.Join(t.TempDir(), "bad")); e != errIntegrity {
				t.Fatalf("got %v want integrity rejection", e)
			}
		})
	}
}
