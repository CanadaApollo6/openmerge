package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// Frozen by running the deployed encoder and actual cursor struct on origin/main
// before slice 2 (see dependency/README.md). No Postgres/forge access is required.
func TestDependencyLegacySnapshotAndCursorGolden(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	state := queue.Apply(queue.State{}, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"})
	state = queue.Apply(state, queue.EnqueueRequested{PR: 2, At: at})
	snapshot, err := dependency.EncodeSnapshot(state, dependency.State{})
	if err != nil {
		t.Fatal(err)
	}
	progress := outsidePushProgress{ScannedThrough: 7, AuditedBaseMove: 5, State: snapshot, ObservedBase: "base", LastLanding: outsideLanding{AttemptID: "attempt", SHA: "head", BaseSHA: "base", Consumed: true}, Pending: []pendingOutsideMove{}}
	cursor, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{"legacy-held.snapshot.json": snapshot, "legacy-held.cursor.json": cursor} {
		want, err := os.ReadFile(filepath.Join("..", "dependency", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("legacy %s bytes changed", name)
		}
	}
	// All deployed event/action fixtures still use the same strict codecs/bytes.
	paths, err := filepath.Glob("../queue/testdata/wire/v1/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatal("missing wire goldens", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var encoded []byte
		if bytes.HasPrefix([]byte(filepath.Base(path)), []byte("event_")) {
			e, err := queue.DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			_, encoded, err = queue.EncodeEvent(e)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			a, err := queue.DecodeAction(raw)
			if err != nil {
				t.Fatal(err)
			}
			_, encoded, err = queue.EncodeAction(a)
			if err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(raw, encoded) {
			t.Fatalf("legacy wire changed: %s", path)
		}
	}
}
