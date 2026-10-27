package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func retainedSnapshot(t *testing.T, s State) State {
	t.Helper()
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnapshot(raw, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	again, err := EncodeSnapshot("queue", decoded.Base, decoded.Provenance)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatalf("compaction snapshot diverged: %v", err)
	}
	return decoded.Provenance
}

func TestRetentionThousandsOfSettledAndSupersededGenerations(t *testing.T) {
	h, _ := fixture(t)
	var s State
	var prior uint64
	for i := 0; i < 2000; i++ {
		id := uint64(i*4 + 3)
		h.Request.Generation = fmt.Sprint(i)
		h.PreviousObservationID = prior
		h.Digest = InventoryDigest(h, h.Commits)
		s = apply(t, s, id, h)
		l := landing(h, []uint64{id})
		l.AttemptID = fmt.Sprint("attempt-", i)
		l.DecisionID = id - 1
		if i%2 == 0 {
			s = apply(t, s, id+1, l)
			l.Stage = "landed"
			l.Members[0].Mapping.Build.Stage = "landed"
			l.Members[0].Mapping.Build.ID = "composition-L"
			l.Members[0].RecordDigest = MemberDigest(l.Members[0])
			s = apply(t, s, id+2, l)
		}
		if len(s.records) > 1 {
			t.Fatal("settled/superseded history accumulated")
		}
		prior = id
		if i%200 == 0 {
			s = retainedSnapshot(t, s)
		}
	}
	s = retainedSnapshot(t, s)
	if len(s.retired) != 1999 || len(s.settled) != 1000 {
		t.Fatalf("lost compact fences: %d/%d", len(s.retired), len(s.settled))
	}
	// The active last generation still requires its exact CAS predecessor.
	h.Request.Generation = "new"
	h.PreviousObservationID = 3
	h.Digest = InventoryDigest(h, h.Commits)
	rejectUnchanged(t, s, 9000, h, ErrBinding)
	h.PreviousObservationID = prior
	h.Request.Generation = "0"
	h.Digest = InventoryDigest(h, h.Commits)
	rejectUnchanged(t, s, 9000, h, ErrBinding)
	l := landing(h, []uint64{3})
	l.AttemptID = "attempt-0"
	rejectUnchanged(t, s, 9000, l, ErrBinding)
}

func TestRetentionPinsUnfinishedAttemptAcrossSupersession(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	s = apply(t, s, 11, l)
	newer := h
	newer.Request.Generation, newer.PreviousObservationID = "new", 3
	newer.Digest = InventoryDigest(newer, newer.Commits)
	s = apply(t, s, 13, newer)
	s = retainedSnapshot(t, s)
	if _, ok := s.Inventory(h.Request); !ok {
		t.Fatal("unfinished attempt lost inventory")
	}
	// Landing may refresh a generation while preserving the original revision/head.
	l.Stage = "landed"
	l.Members[0].Request = newer.Request
	l.Members[0].Mapping.Request = newer.Request
	l.Members[0].ObservationIDs = []uint64{13}
	l.Members[0].InventoryDigest = newer.Digest
	l.Members[0].Mapping.Build.Stage = "landed"
	l.Members[0].Mapping.Build.ID = "composition-L"
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	s = apply(t, s, 15, l)
	if len(s.records) != 0 {
		t.Fatal("completed attempt retained raw records")
	}
	retainedSnapshot(t, s)
}

func TestRetentionPendingBoundAndCompactSnapshotCorruption(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	for i := 0; i < MaxPendingLandings; i++ {
		l := landing(h, []uint64{3})
		l.AttemptID = fmt.Sprint(i)
		s = apply(t, s, uint64(i+11), l)
	}
	l := landing(h, []uint64{3})
	l.AttemptID = "overflow"
	if _, err := Apply(s, 100, l); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded pending attempts", err)
	}
	// Supersession creates a checkpoint while keeping all pending references.
	h.Request.Generation, h.PreviousObservationID = "new", 3
	h.Digest = InventoryDigest(h, h.Commits)
	s = apply(t, s, 101, h)
	// Complete one attempt to exercise retired markers in the new snapshot shape.
	l = landing(h, []uint64{101})
	l.AttemptID = "0"
	l.Stage = "landed"
	l.Members[0].Mapping.Build.Stage = "landed"
	l.Members[0].Mapping.Build.ID = "composition-L"
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	s = apply(t, s, 102, l)
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	var v compactSnapshot
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*compactSnapshot){
		"future fence":              func(v *compactSnapshot) { v.Compaction.Settled[0].ID = 103 },
		"watermark":                 func(v *compactSnapshot) { v.Compaction.Through++ },
		"duplicate fence":           func(v *compactSnapshot) { v.Compaction.Settled = append(v.Compaction.Settled, v.Compaction.Settled[0]) },
		"pending snapshot overflow": func(v *compactSnapshot) { v.Records = append(v.Records, v.Records[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			var next compactSnapshot
			if err := json.Unmarshal(raw, &next); err != nil {
				t.Fatal(err)
			}
			mutate(&next)
			b, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(b, func(string, []byte) error { return nil }); err == nil {
				t.Fatal("accepted corrupt checkpoint")
			}
		})
	}
}

func TestRetentionReferenceBudgetPrecedesLookup(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	l.DecisionID = 1
	refs := make([]uint64, MaxRetainedReferences)
	for i := range refs {
		refs[i] = uint64(i + 1)
	}
	l.Members[0].Layers[0].References = refs
	l.Members[0].Layers[2].References = []uint64{2}
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	s = apply(t, s, 1000, l)
	if err := s.Preflight(); err != nil {
		t.Fatal(err)
	}
	restored := retainedSnapshot(t, s)
	if len(restored.landings) != 1 {
		t.Fatal("budgeted companion lost")
	}
	l.AttemptID = "overflow"
	if _, err := Apply(s, 1001, l); !errors.Is(err, ErrLimit) {
		t.Fatal("aggregate reference budget ignored", err)
	}
	l.Members[0].Layers[0].References = append(slices.Clone(refs), 257)
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	if _, err := Apply(State{}, 1001, l); !errors.Is(err, ErrLimit) {
		t.Fatal("single-record reference budget ignored", err)
	}
}
