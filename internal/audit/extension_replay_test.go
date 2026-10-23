package audit

import (
	"path/filepath"
	"testing"
)

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
