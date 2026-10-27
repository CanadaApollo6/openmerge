package store

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestProvenanceLegacyHeldSnapshotAndCursorGolden(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	q := queue.Apply(queue.State{}, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"})
	q = queue.Apply(q, queue.EnqueueRequested{PR: 2, At: at})
	raw, err := queue.EncodeSnapshot(q)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := journal.DecodeSnapshot(raw, func(_ string, b []byte) error { var err error; q, err = queue.DecodeSnapshot(b); return err })
	if err != nil {
		t.Fatal(err)
	}
	if len(q.HeldRequests()) != 1 {
		t.Fatal("lost held request")
	}
	again, err := queue.EncodeSnapshot(q)
	if err != nil || !bytes.Equal(again, raw) {
		t.Fatal("snapshot changed", err)
	}
	progress := outsidePushProgress{ScannedThrough: 7, AuditedBaseMove: 5, State: decoded.Base, ObservedBase: "base", LastLanding: outsideLanding{AttemptID: "attempt", SHA: "head", BaseSHA: "base", Consumed: true}, Pending: []pendingOutsideMove{}}
	cursor, err := json.Marshal(progress)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{"legacy-held.snapshot.json": again, "legacy-held.cursor.json": cursor} {
		want, err := os.ReadFile("../dependency/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("legacy %s changed", name)
		}
	}
	// Existing dependency compatibility coverage also pins all deployed event and
	// action fixtures; this test uses the actual unchanged cursor type above.
}
