package audit

import (
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestClosureReplayPinsWatermarkAcrossQueuePageBoundary(t *testing.T) {
	f := database(t)
	f.queue(t, 1, "")
	f.queue(t, 2, "z")
	// Queue order differs from journal insertion order. The second queue's
	// watermark is older than the first queue's required forward dependency.
	secondQueueMax := insert(t, f.admin, 2, "2026-10-22T12:00:00Z")
	closureEvent(t, f, "2026-10-22T12:01:00Z", queue.LandingStarted{BatchID: "batch", AttemptID: "attempt"})
	firstQueueMax := closureEvent(t, f, "2026-10-22T14:00:00Z", queue.Landed{BatchID: "batch", NewBaseSHA: "base", NewFence: "fence"})
	if firstQueueMax <= secondQueueMax {
		t.Fatal("fixture does not distinguish queue watermarks")
	}
	out := filepath.Join(t.TempDir(), "export")
	m, err := f.reader.Export(t.Context(), req(), out)
	must(t, err)
	if m.Count != 3 {
		t.Fatalf("count=%d want three sources across two queues", m.Count)
	}
	replay := filepath.Join(t.TempDir(), "replay")
	_, err = f.reader.Replay(t.Context(), filepath.Join(out, "manifest.json"), replay)
	must(t, err)
	manifests(t, out, replay)
}
