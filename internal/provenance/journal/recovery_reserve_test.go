package journal

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
)

func TestReconstructedRecoveryPreservesRetirementReserve(t *testing.T) {
	// This supported option must not admit a state that ordinary retirement makes unreadable.
	s := State{groups: map[string]group{}, latest: map[string]string{}, retired: map[string]uint64{}, settled: map[string]uint64{}}
	h, _ := fixture(t)
	h.Request.Repository = strings.Repeat("\x01", 256)
	h.Request.Queue = strings.Repeat("\x02", 256)
	h.Request.Generation = strings.Repeat("\x04", 256)
	for i := 0; ; i++ {
		h.Request.ChangeID = strings.Repeat("\x03", 249) + fmt.Sprintf("%07d", i)
		key := generationKey(h.Request)
		id := uint64(i + 1)
		charge := fenceSize(key, id)
		if s.fenceBytes+charge > maxFenceTableBytes*99/100 {
			break
		}
		s.retired[key] = id
		s.fenceBytes += charge
		s.latest[changeKey(h.Request)] = key
		s.lastID = id
	}
	// Materialize 500 independently valid active inventories, retaining canonical records.
	for i := 0; i < 500; i++ {
		h.Request.ChangeID = strings.Repeat("\x05", 249) + fmt.Sprintf("%07d", i)
		h.Digest = InventoryDigest(h, h.Commits)
		_, raw, err := EncodeEvent(h)
		if err != nil {
			t.Fatal(err)
		}
		key := generationKey(h.Request)
		s.lastID++
		id := s.lastID
		s.groups[key] = group{header: h, commits: h.Commits, ids: []uint64{id}}
		s.latest[changeKey(h.Request)] = key
		s.records = append(s.records, Record{id, raw})
		s.bytes += len(raw)
	}

	// Remove candidates from the tail until the exact next-generation boundary.
	total := s.fenceBytes
	for key, g := range s.groups {
		total += fenceSize(key, g.ids[0])
	}
	for total > maxFenceTableBytes {
		last := s.records[len(s.records)-1]
		e, err := DecodeEvent(last.Payload)
		if err != nil {
			t.Fatal(err)
		}
		key := generationKey(e.(Observation).Request)
		g := s.groups[key]
		total -= fenceSize(key, g.ids[0])
		delete(s.groups, key)
		s.bytes -= len(last.Payload)
		s.records = s.records[:len(s.records)-1]
	}

	var combined Landing
	for i := 0; i < 3; i++ {
		small, _ := fixture(t)
		small.Request.ChangeID = fmt.Sprint("recovery-member-", i)
		small.Digest = InventoryDigest(small, small.Commits)
		id := s.lastID + 1
		s = apply(t, s, id, small)
		l := landing(small, []uint64{id})
		if i == 0 {
			combined = l
			combined.Members = nil
		}
		combined.Members = append(combined.Members, l.Members[0])
	}
	s = apply(t, s, s.lastID+1, combined)
	if err := s.Preflight(99); err != nil {
		t.Fatal("initial admission refused", err)
	}
	s = AwaitLandedOwned(s, s.lastID+1, combined.BatchID, combined.AttemptID)
	s = retainedSnapshot(t, s)
	before := s.Capacity(99)
	// Reconstruct only inventories needed by this recovery: no unrelated
	// evidence remains. The new generations themselves consume reserved fences.
	for i := range combined.Members {
		m := &combined.Members[i]
		rebuilt, _ := fixture(t)
		rebuilt.Request = m.Request
		rebuilt.PreviousObservationID = m.ObservationIDs[0]
		rebuilt.Request.Generation = strings.Repeat("\x06", 256)
		rebuilt.Digest = InventoryDigest(rebuilt, rebuilt.Commits)
		id := s.lastID + 1
		s = apply(t, s, id, rebuilt)
		m.Request = rebuilt.Request
		m.Mapping.Request = rebuilt.Request
		m.ObservationIDs = []uint64{id}
		m.InventoryDigest = rebuilt.Digest
	}
	combined.Stage = "landed"
	for i := range combined.Members {
		m := &combined.Members[i]
		m.Mapping.Build.Stage = "landed"
		m.Mapping.Build.ID = "landed"
		m.RecordDigest = MemberDigest(*m)
	}
	s = apply(t, s, s.lastID+1, combined)
	if _, settled := s.SettledAttempt(combined.AttemptID); !settled {
		t.Fatal("not settled")
	}
	after := s.Capacity(99)
	if after.ProjectedBytes >= before.ProjectedBytes {
		t.Fatal("fixture did not shrink", before, after)
	}
	if err := s.PreflightRecovery(before); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "queue retirement reserve") {
		t.Fatal("unsafe reconstructed recovery admitted", err)
	}
	// Without the reserve guard this is a strict, reloadable projection that
	// becomes unencodable on the next accepted queue retirement.
	if !testing.Short() {
		s = retainedSnapshot(t, s)
	}
	s = RetireOwned(s, s.lastID+1, "", func(p.InventoryRequest) bool { return true })
	if s.fenceBytes-maxFenceTableBytes != 2366 {
		t.Fatal("fixture lost exact hard-cap overrun", s.fenceBytes, maxFenceTableBytes)
	}
	if _, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s); !errors.Is(err, ErrLimit) {
		t.Fatal("unsafe fixture unexpectedly encodable", err)
	}
	t.Logf("refused shrinking recovery (%d -> %d bytes) that would overrun retirement by 2366 bytes", before.ProjectedBytes, after.ProjectedBytes)
}
