package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
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

// Capacity tests exercise the real limits in both modes. Short mode seeds a
// compact checkpoint and combines disjoint, individually validated inventories
// instead of replaying an ever-growing active map for every setup event.
func capacityFixture(t *testing.T) State {
	t.Helper()
	if !testing.Short() {
		return State{}
	}
	h, _ := fixture(t)
	h.Request.ChangeID = "capacity-seed"
	h.Digest = InventoryDigest(h, h.Commits)
	s := apply(t, State{}, 1, h)
	return RetireOwned(s, 2, "", func(p.InventoryRequest) bool { return true })
}

func addCapacityInventory(t *testing.T, s State, id uint64, h Observation) State {
	t.Helper()
	if !testing.Short() || len(s.records) == maxRecords-1 {
		next, err := ApplyOwned(s, id, h)
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	part := apply(t, State{}, id, h)
	if len(part.records) != 1 || len(part.groups) != 1 || s.latest[changeKey(h.Request)] != "" || id <= s.lastID {
		t.Fatal("capacity fixture requires disjoint, ordered single-record inventories")
	}
	s.records = append(s.records, part.records...)
	maps.Copy(s.groups, part.groups)
	maps.Copy(s.latest, part.latest)
	s.bytes += part.bytes
	s.lastID = id
	return s
}

func TestRetentionThousandsOfSettledAndSupersededGenerations(t *testing.T) {
	n := 2000
	if testing.Short() {
		n = 200
	}
	h, _ := fixture(t)
	var s State
	var prior uint64
	for i := 0; i < n; i++ {
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
		if (i+1)%(n/10) == 0 {
			s = retainedSnapshot(t, s)
		}
	}
	s = retainedSnapshot(t, s)
	if len(s.retired) != n-1 || len(s.settled) != n/2 {
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
	refs := make([]uint64, MaxLandingReferences)
	for i := range refs {
		refs[i] = uint64(i + 1)
	}
	l.Members[0].Layers[0].References = refs
	l.Members[0].Layers[2].References = []uint64{2}
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	for i := 0; i < MaxPendingLandings; i++ {
		l.AttemptID = fmt.Sprint(i)
		s = apply(t, s, uint64(1000+i), l)
	}
	if err := s.Preflight(); err != nil {
		t.Fatal(err)
	}
	restored := retainedSnapshot(t, s)
	if len(restored.landings) != MaxPendingLandings {
		t.Fatal("budgeted companion lost")
	}
	l.AttemptID = "overflow"
	if _, err := Apply(s, 20000, l); !errors.Is(err, ErrLimit) {
		t.Fatal("aggregate reference budget ignored", err)
	}
	l.Members[0].Layers[0].References = append(slices.Clone(refs), uint64(MaxLandingReferences+1))
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	if _, err := Apply(State{}, 20000, l); !errors.Is(err, ErrLimit) {
		t.Fatal("single-record reference budget ignored", err)
	}
}

func TestSettledAttemptCannotBeReusedWithFreshInventory(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	s = apply(t, s, 11, l)
	l.Stage = "landed"
	l.Members[0].Mapping.Build.Stage = "landed"
	l.Members[0].Mapping.Build.ID = "composition-L"
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	s = apply(t, s, 12, l)
	h.Request.Generation = "fresh"
	h.PreviousObservationID = 3
	h.Digest = InventoryDigest(h, h.Commits)
	s = apply(t, retainedSnapshot(t, s), 13, h)
	l = landing(h, []uint64{13})
	rejectUnchanged(t, s, 20, l, ErrBinding)
}

func TestCheckpointRejectsBrokenGenerationChainAndTemporalBinding(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	s = apply(t, s, 11, l)
	h.Request.Generation = "new"
	h.PreviousObservationID = 3
	h.Digest = InventoryDigest(h, h.Commits)
	s = apply(t, s, 13, h)
	// Force a checkpoint by retiring an unrelated generation.
	other := h
	other.Request.ChangeID = "other"
	other.Request.Generation = "old"
	other.PreviousObservationID = 0
	other.Digest = InventoryDigest(other, other.Commits)
	s = apply(t, s, 14, other)
	other.Request.Generation = "new"
	other.PreviousObservationID = 14
	other.Digest = InventoryDigest(other, other.Commits)
	s = apply(t, s, 15, other)
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"chain", "temporal"} {
		t.Run(kind, func(t *testing.T) {
			var v compactSnapshot
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			if kind == "chain" {
				e, err := DecodeEvent(v.Records[2].Payload)
				if err != nil {
					t.Fatal(err)
				}
				obs := e.(Observation)
				obs.PreviousObservationID = 2
				obs.Digest = InventoryDigest(obs, obs.Commits)
				_, v.Records[2].Payload, err = EncodeEvent(obs)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				v.Records[1].ID = 16
				v.LastID, v.Compaction.Through = 16, 16
				slices.SortFunc(v.Records, func(a, b Record) int {
					if a.ID < b.ID {
						return -1
					}
					if a.ID > b.ID {
						return 1
					}
					return 0
				})
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(b, func(string, []byte) error { return nil }); err == nil {
				t.Fatal("accepted corrupt checkpoint", kind)
			}
		})
	}
}

func TestOwnedFoldScalePreservesFenceMaps(t *testing.T) {
	h, _ := fixture(t)
	var s State
	var prior uint64
	var retiredMap, settledMap, latestMap uintptr
	n := 20000
	if testing.Short() {
		n = 400
	}
	for i := 0; i < n; i++ {
		id := uint64(i*4 + 3)
		h.Request.Generation = fmt.Sprint(i)
		h.PreviousObservationID = prior
		h.Digest = InventoryDigest(h, h.Commits)
		var err error
		s, err = ApplyOwned(s, id, h)
		if err != nil {
			t.Fatal(err)
		}
		l := landing(h, []uint64{id})
		l.AttemptID = fmt.Sprint(i)
		l.DecisionID = id - 1
		s, err = ApplyOwned(s, id+1, l)
		if err != nil {
			t.Fatal(err)
		}
		l.Stage = "landed"
		l.Members[0].Mapping.Build.Stage = "landed"
		l.Members[0].Mapping.Build.ID = "composition-L"
		l.Members[0].RecordDigest = MemberDigest(l.Members[0])
		s, err = ApplyOwned(s, id+2, l)
		if err != nil {
			t.Fatal(err)
		}
		currentRetired, currentSettled, currentLatest := reflect.ValueOf(s.retired).Pointer(), reflect.ValueOf(s.settled).Pointer(), reflect.ValueOf(s.latest).Pointer()
		if i > 0 && (currentRetired != retiredMap || currentSettled != settledMap || currentLatest != latestMap) {
			t.Fatal("fold copied a lifetime map")
		}
		retiredMap, settledMap, latestMap = currentRetired, currentSettled, currentLatest
		prior = id
		if len(s.records) != 0 || len(s.retired) != i+1 || len(s.settled) != i+1 {
			t.Fatal("nonlinear retained state")
		}
		if (i+1)%(n/10) == 0 {
			t.Logf("%d attempts: %d retired, %d settled, 0 records; 0 lifetime-map copies", i+1, len(s.retired), len(s.settled))
		}
	}
}

func TestMaximumBatchAtMaximumSpeculationDepthFitsReferenceBudget(t *testing.T) {
	if MaxPendingLandings != queue.MaxSpeculationDepth || MaxBatchMembers != queue.MaxBatchSize {
		t.Fatal("retention no longer covers the accepted configuration maxima")
	}
	maxReferences := MaxPendingLandings * (MaxBatchMembers*ReferencesPerMember + 1)
	maxRecords := MaxPendingLandings * (MaxBatchMembers + 1)
	if maxReferences > MaxRetainedReferences || maxRecords+1 > MaxActiveRecords {
		t.Fatal("maximum configuration exceeds reference or record budget")
	}
	// Encode one maximum-width attempt. The final indices have the longest
	// identities, and uint64 observation positions conservatively bound IDs.
	h, _ := fixture(t)
	maximum := Landing{AttemptID: fmt.Sprint(MaxPendingLandings - 1), BatchID: fmt.Sprint("batch-", MaxPendingLandings-1), DecisionID: 1, Stage: "tested"}
	inventoryBytes := 0
	for member := range MaxBatchMembers {
		h.Request.ChangeID = fmt.Sprintf("member-%d-%d", MaxPendingLandings-1, member)
		h.Digest = InventoryDigest(h, h.Commits)
		_, raw, err := EncodeEvent(h)
		if err != nil {
			t.Fatal(err)
		}
		inventoryBytes += len(raw)
		m := landing(h, []uint64{^uint64(0) - uint64(member)}).Members[0]
		for layer := range m.Layers {
			m.Layers[layer].References = []uint64{uint64(2*member + 2), uint64(2*member + 3)}
		}
		m.RecordDigest = MemberDigest(m)
		maximum.Members = append(maximum.Members, m)
	}
	_, raw, err := EncodeEvent(maximum)
	if err != nil {
		t.Fatal(err)
	}
	maximumBytes := MaxPendingLandings * (inventoryBytes + len(raw))
	if ReferenceCount(maximum) != MaxBatchMembers*ReferencesPerMember+1 || maximumBytes+128 >= maxSnapshotBytes/2*DefaultHighWaterPercent/100 {
		t.Fatal("maximum fixture exceeds reference or byte budget", maximumBytes)
	}
	attempts, batchSize := MaxPendingLandings, MaxBatchMembers
	if testing.Short() {
		attempts, batchSize = 4, 8
	}
	s := capacityFixture(t)
	var id uint64 = 1000
	for attempt := 0; attempt < attempts; attempt++ {
		members := make([]Member, batchSize)
		for member := range members {
			h.Request.ChangeID = fmt.Sprintf("member-%d-%d", attempt, member)
			h.Digest = InventoryDigest(h, h.Commits)
			id++
			s = addCapacityInventory(t, s, id, h)
			m := landing(h, []uint64{id}).Members[0]
			for layer := range m.Layers {
				m.Layers[layer].References = []uint64{uint64(2*member + 2), uint64(2*member + 3)}
			}
			m.RecordDigest = MemberDigest(m)
			members[member] = m
		}
		l := Landing{AttemptID: fmt.Sprint(attempt), BatchID: fmt.Sprint("batch-", attempt), DecisionID: 1, Stage: "tested", Members: members}
		if ReferenceCount(l) != batchSize*ReferencesPerMember+1 {
			t.Fatal("maximum-envelope fixture lost references")
		}
		id++
		var err error
		s, err = ApplyOwned(s, id, l)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Preflight(); err != nil {
		t.Fatal(err)
	}
	if s.bytes > maximumBytes {
		t.Fatal("real run exceeds the maximum byte estimate", s.bytes, maximumBytes)
	}
	if len(s.records) != attempts*(batchSize+1) {
		t.Fatal("unexpected inventory/companion work count")
	}
	s = retainedSnapshot(t, s)
	if len(s.landings) != attempts || len(s.groups) != attempts*batchSize {
		t.Fatal("maximum configuration did not survive restart")
	}
	t.Logf("maximum: %d records, %d references, <=%d payload bytes; replay/reload: %d members x %d attempts, %d records", maxRecords, maxReferences, maximumBytes, batchSize, attempts, len(s.records))
}

func TestOwnedRejectionRollsBackLifetimeFenceUpdates(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	s.fenceBytes = maxSnapshotBytes / 2
	// Compaction will retire the old generation before discovering the byte
	// ceiling. Neither that fence nor the latest-generation CAS may leak.
	before, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	h.Request.Generation = "new"
	h.PreviousObservationID = 3
	h.Digest = InventoryDigest(h, h.Commits)
	next, err := ApplyOwned(s, 5, h)
	if !errors.Is(err, ErrLimit) {
		t.Fatal("fixture did not hit capacity", err)
	}
	after, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), next)
	if err != nil {
		t.Fatal(err)
	}
	original, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(before, original) {
		t.Fatal("owned rejection leaked a fence update")
	}
}

func TestCheckpointRejectsPendingBeforeFinalInventoryChunk(t *testing.T) {
	h, commits := fixture(t)
	parts := chunks(h, commits)
	s := apply(t, State{}, 3, parts[0])
	s = apply(t, s, 5, parts[1])
	l := landing(h, []uint64{3, 5})
	s = apply(t, s, 11, l)
	// Supersede unrelated work to select the compact checkpoint decoder.
	other := h
	other.Request.ChangeID = "other"
	other.Digest = InventoryDigest(other, other.Commits)
	s = apply(t, s, 12, other)
	other.Request.Generation, other.PreviousObservationID = "next", 12
	other.Digest = InventoryDigest(other, other.Commits)
	s = apply(t, s, 13, other)
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw, func(string, []byte) error { return nil }); err != nil {
		t.Fatal("valid multi-chunk checkpoint rejected", err)
	}
	var forged compactSnapshot
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	// Keep chunk zero before the companion, but move its final chunk after it.
	// All IDs, payloads, references and the generation's first-ID ordering remain
	// valid individually; only the inventory-completion ordering is forged.
	for i := range forged.Records {
		if forged.Records[i].ID == 5 {
			forged.Records[i].ID = 14
		}
		if forged.Records[i].ID == 11 {
			l.Members[0].ObservationIDs = []uint64{3, 14}
			l.Members[0].RecordDigest = MemberDigest(l.Members[0])
			_, forged.Records[i].Payload, err = EncodeEvent(l)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	forged.LastID, forged.Compaction.Through = 14, 14
	slices.SortFunc(forged.Records, func(a, b Record) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	raw, err = json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw, func(string, []byte) error { return nil }); !errors.Is(err, ErrBinding) {
		t.Fatalf("accepted pending companion before its final inventory chunk: %v", err)
	}
}
