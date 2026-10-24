package audit

import (
	"bytes"
	"encoding/json"
	"os"
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
	for _, name := range []string{"identity", "bad-role", "empty-role", "case-role", "dependency-role", "null-references", "nonempty-references", "cross-queue-order"} {
		t.Run(name, func(t *testing.T) {
			f := database(t)
			f.queue(t, 1, "")
			f.queue(t, 2, "z")
			insert(t, f.admin, 1, "2026-10-22T12:00:00Z")
			insert(t, f.admin, 2, "2026-10-22T12:00:00Z")
			original := filepath.Join(t.TempDir(), "original")
			_, e := f.reader.Export(t.Context(), req(), original)
			must(t, e)
			mutated := copyExport(t, original)
			rewriteSelections(t, mutated, func(selections []Selection) {
				switch name {
				case "bad-role":
					selections[0].Role = "invalid"
				case "empty-role":
					selections[0].Role = ""
				case "case-role":
					selections[0].Role = "Scope"
				case "dependency-role":
					selections[0].Role = "dependency"
				case "null-references":
					selections[0].References = nil
				case "nonempty-references":
					selections[0].References = []Reference{{Identity: evidenceIdentity("invalid")}}
				case "cross-queue-order":
					selections[0], selections[1] = selections[1], selections[0]
				}
			})
			_, e = f.reader.Replay(t.Context(), filepath.Join(mutated, "manifest.json"), filepath.Join(t.TempDir(), "bad"))
			if name == "identity" {
				// Control: the forger itself is coherent, so an unchanged
				// selection set replays and later rejections are semantic.
				must(t, e)
				return
			}
			if e != errIntegrity {
				t.Fatalf("got %v want integrity rejection", e)
			}
		})
	}
}

// rewriteSelections forges a fully coherent artifact around changed selections:
// metadata digests, the chained events stream (which embeds roles and
// references), dependency boundaries, stream digests, count, terminal and the
// manifest self-hash. It deliberately bypasses build's own validation, so only
// Replay's semantic guards can reject the result.
func rewriteSelections(t *testing.T, dir string, change func([]Selection)) {
	t.Helper()
	type key struct{ queue, id int64 }
	payloads := map[key]json.RawMessage{}
	must(t, lines(bytes.NewReader(read(t, filepath.Join(dir, "events.jsonl"))), func(raw []byte) error {
		var v envelope
		if e := json.Unmarshal(raw, &v); e != nil {
			return e
		}
		payloads[key{v.Source.QueueID, v.Source.ID}] = v.Payload
		return nil
	}))
	selections := readSelections(t, dir)
	change(selections)
	type boundary struct {
		QueueID int64 `json:"queue_id"`
		Queue   Key   `json:"queue"`
		First   int64 `json:"first"`
		Last    int64 `json:"last"`
		Count   int64 `json:"count"`
	}
	var selected, events, boundaries bytes.Buffer
	line := func(b *bytes.Buffer, v any) []byte {
		raw, e := encode(v)
		must(t, e)
		b.Write(raw)
		b.WriteByte('\n')
		return raw
	}
	var current *boundary
	terminal := zeroDigest
	for i := range selections {
		s := &selections[i]
		meta, e := metadata(*s)
		must(t, e)
		s.MetadataSHA256 = digest(meta)
		if s.Role == "dependency" {
			if current != nil && current.QueueID != s.Source.QueueID {
				line(&boundaries, current)
				current = nil
			}
			if current == nil {
				current = &boundary{QueueID: s.Source.QueueID, Queue: s.Source.Queue, First: s.Source.ID}
			}
			current.Last = s.Source.ID
			current.Count++
		}
		payload, ok := payloads[key{s.Source.QueueID, s.Source.ID}]
		if !ok {
			t.Fatal("forged selection has no retained payload", s.Source)
		}
		terminal = digest(line(&events, envelope{Kind: "source", Source: s.Source, Payload: payload, Role: s.Role, References: s.References, Kinds: s.Kinds, PreviousSHA256: terminal}))
		line(&selected, s)
	}
	if current != nil {
		line(&boundaries, current)
	}
	for name, b := range map[string][]byte{"selection.jsonl": selected.Bytes(), "events.jsonl": events.Bytes(), "dependency_boundaries.jsonl": boundaries.Bytes()} {
		must(t, os.WriteFile(filepath.Join(dir, name), b, 0600))
	}
	rewriteManifest(t, dir, func(m *Manifest) {
		m.SelectionSHA256, m.EventsSHA256, m.DependenciesSHA256 = digest(selected.Bytes()), digest(events.Bytes()), digest(boundaries.Bytes())
		m.TerminalSHA256, m.Count = terminal, int64(len(selections))
	})
}
