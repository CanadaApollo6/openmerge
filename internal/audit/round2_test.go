package audit

import (
	"bytes"
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
	for _, name := range []string{"bad-role", "empty-role", "case-role", "dependency-role", "null-references", "nonempty-references", "cross-queue-order"} {
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
					selections[0].References = []Reference{{}}
				case "cross-queue-order":
					selections[0], selections[1] = selections[1], selections[0]
				}
			})
			if _, e = f.reader.Replay(t.Context(), filepath.Join(mutated, "manifest.json"), filepath.Join(t.TempDir(), "bad")); e != errIntegrity {
				t.Fatalf("got %v want integrity rejection", e)
			}
		})
	}
}

// rewriteSelections keeps the artifact's selection and manifest digests
// internally consistent so Replay's semantic guards own these rejections.
func rewriteSelections(t *testing.T, dir string, change func([]Selection)) {
	t.Helper()
	selections := readSelections(t, dir)
	change(selections)
	var b bytes.Buffer
	for i := range selections {
		meta, e := metadata(selections[i])
		must(t, e)
		selections[i].MetadataSHA256 = digest(meta)
		raw, e := encode(selections[i])
		must(t, e)
		b.Write(raw)
		b.WriteByte('\n')
	}
	contents := b.Bytes()
	must(t, os.WriteFile(filepath.Join(dir, "selection.jsonl"), contents, 0600))
	rewriteManifest(t, dir, func(m *Manifest) { m.SelectionSHA256 = digest(contents) })
}
