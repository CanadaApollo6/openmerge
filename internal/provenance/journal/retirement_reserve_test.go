package journal

import (
	"errors"
	"fmt"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"strings"
	"testing"
)

// Conservatively charge all remaining raw generation/attempt fences at once.
// Active evidence is itself capped, and no queue event can introduce an inventory.
func TestTerminalReserveAtActiveRecordLimit(t *testing.T) {
	h, _ := fixture(t)
	h.Request.Repository = strings.Repeat("\x01", 256)
	h.Request.Queue = strings.Repeat("\x02", 256)
	h.Request.ChangeID = strings.Repeat("\x03", 256)
	h.Request.Generation = strings.Repeat("\x04", 256)
	h.Digest = InventoryDigest(h, h.Commits)
	_, raw, err := EncodeEvent(h)
	if err != nil {
		t.Fatal(err)
	}
	size := fenceSize(generationKey(h.Request), ^uint64(0))
	t.Logf("max escaped generation fence charge=%d, complete inventory payload=%d", size, len(raw))
	h.Complete = false
	h.Diagnostic = p.Diagnostic{Outcome: p.ReadPending, Resource: "x"}
	h.DeclarationDigest = ""
	h.Commits = []p.Commit{}
	h.Stable = []string{}
	h.Additional = []string{}
	h.MergeBases = []string{}
	h.BuildMergeBases = []string{}
	h.Digest = InventoryDigest(h, nil)
	_, raw, err = EncodeEvent(h)
	if err != nil {
		t.Fatal(err)
	}
	if size > len(raw) {
		t.Fatalf("generation fence grows beyond inventory budget: %d > %d", size, len(raw))
	}
	reserveEntries := maxFenceEntries - maxFenceEntries*80/100
	if reserveEntries < MaxActiveRecords {
		t.Fatal("insufficient entry reserve")
	}
	t.Logf("minimal pending payload=%d, expanded budget non-increasing after transferring active payload charge; entry reserve=%d, active record limit=%d", len(raw), reserveEntries, MaxActiveRecords)
	// Exercise every active-record slot except preflight's single-record reserve.
	s := State{groups: map[string]group{}, latest: map[string]string{}, retired: map[string]uint64{}, settled: map[string]uint64{}}
	for i := 0; i < MaxActiveRecords-1; i++ {
		h.Request.Repository = "r"
		h.Request.Queue = "q"
		h.Request.ChangeID = fmt.Sprint("change-", i)
		h.Request.Generation = "generation"
		h.Digest = InventoryDigest(h, nil)
		_, raw, err := EncodeEvent(h)
		if err != nil {
			t.Fatal(err)
		}
		key := generationKey(h.Request)
		id := uint64(i + 1)
		s.groups[key] = group{header: h, commits: []p.Commit{}, ids: []uint64{id}}
		s.latest[changeKey(h.Request)] = key
		s.records = append(s.records, Record{id, raw})
		s.bytes += len(raw)
		s.lastID = id
	}
	if err := s.Preflight(); err != nil {
		t.Fatal(err)
	}
	before := s.Capacity(80)
	s = RetireOwned(s, s.lastID+1, "", func(p.InventoryRequest) bool { return true })
	after := s.Capacity(80)
	raw, err = EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(retainedSnapshot(t, s).retired) != MaxActiveRecords-1 {
		t.Fatal("retirement lost fences")
	}
	t.Logf("retired %d active inventories at once: projected %d -> %d, snapshot=%d bytes", MaxActiveRecords-1, before.ProjectedBytes, after.ProjectedBytes, len(raw))
}

func TestConfiguredReserveAccountsForActiveInventory(t *testing.T) {
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
	if err := s.Preflight(99); !errors.Is(err, ErrLimit) {
		t.Fatal("unsafe 99% configuration admitted", err)
	}
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnapshot(raw, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal("initial state not reloadable", err)
	}
	s = decoded.Provenance
	t.Logf("reloadable but refused at 99%%: expanded=%d projected=%d fences=%d active=%d", s.fenceBytes, s.Capacity(99).ProjectedBytes, len(s.retired), len(s.records))
	s = RetireOwned(s, s.lastID+1, "", func(p.InventoryRequest) bool { return true })
	_, err = EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("fixture must exceed hard capacity after retirement: expanded=%d limit=%d: %v", s.fenceBytes, maxFenceTableBytes, err)
	}
	// The same percentage remains useful when the current inventory fits its
	// retirement reserve. Rebuild a smaller strict checkpoint from original rows.
	decoded, err = DecodeSnapshot(raw, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s = decoded.Provenance
	for _, r := range s.records[100:] {
		e, err := DecodeEvent(r.Payload)
		if err != nil {
			t.Fatal(err)
		}
		delete(s.groups, generationKey(e.(Observation).Request))
		s.bytes -= len(r.Payload)
	}
	s.records = s.records[:100]
	if err := s.Preflight(99); err != nil {
		t.Fatal("safe 99% inventory refused", err)
	}
	s = retainedSnapshot(t, s)
	s = RetireOwned(s, s.lastID+1, "", func(p.InventoryRequest) bool { return true })
	s = retainedSnapshot(t, s)
	if len(s.groups) != 0 || s.fenceBytes > maxFenceTableBytes {
		t.Fatal("admitted inventory cannot retire safely")
	}
}

func TestRecoveryPreflightKeepsHardCapsAndReduction(t *testing.T) {
	for name, s := range map[string]State{
		"unchanged":        {bytes: 100},
		"growth":           {bytes: 101},
		"fence bytes":      {fenceBytes: maxFenceTableBytes + 1},
		"projection bytes": {bytes: maxSnapshotBytes/2 + 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.PreflightRecovery(CapacityGauge{ProjectedBytes: 100}); !errors.Is(err, ErrLimit) {
				t.Fatal("unsafe recovery admitted", err)
			}
		})
	}
	s := State{bytes: 99}
	if err := s.PreflightRecovery(CapacityGauge{ProjectedBytes: 100}); err != nil {
		t.Fatal(err)
	}
}
