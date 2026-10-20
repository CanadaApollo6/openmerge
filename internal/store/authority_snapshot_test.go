package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestLegacyOutsidePushCursorRemainsV6(t *testing.T) {
	s := newStore(t)
	q := ensureQueue(t, s, "")
	seedOutsideLanding(t, s, q)
	auditPass(t, s, q)
	assertCursor := func() {
		t.Helper()
		raw, err := s.Observation(t.Context(), q, outsidePushCursorKey)
		must(t, err)
		var progress outsidePushProgress
		must(t, json.Unmarshal(raw, &progress))
		var header struct{ Version int }
		must(t, json.Unmarshal(progress.State, &header))
		if header.Version != 6 || bytes.Contains(progress.State, []byte(`"Authority"`)) {
			t.Fatal("legacy persisted cursor crossed v7 boundary")
		}
		state, err := queue.DecodeSnapshot(progress.State)
		must(t, err)
		legacy, err := queue.EncodeSnapshotV6(state)
		must(t, err)
		if !bytes.Equal(legacy, progress.State) {
			t.Fatal("persisted cursor differs from v6 encoding")
		}
	}
	assertCursor()
	appendEvents(t, s, q, queue.LandingAbandoned{BatchID: "b1", NewFence: "fence2"}, queue.BaseMoved{SHA: "outside"})
	auditPass(t, s, q)
	assertCursor()
	// Reloading the persisted cursor must still use the same legacy encoding.
	auditPass(t, s, q)
	assertCursor()
}
