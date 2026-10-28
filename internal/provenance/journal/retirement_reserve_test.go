package journal

import (
	"errors"
	"fmt"
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"strconv"
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
	if testing.Short() {
		testRetirementReserveBoundaries(t)
		return
	}
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

func TestRetirementReserveEntryCap(t *testing.T) {
	// Only map cardinality matters to this guard. Avoid replaying, encoding or
	// compressing millions of fences; the reduced checkpoint below tests that
	// active evidence transfers to real fences on retirement.
	s := State{
		retired: make(map[string]uint64, maxFenceEntries-2),
		groups:  map[string]group{"active": {ids: []uint64{1}}},
		landings: map[string]Landing{
			"first": {AttemptID: "first"},
		},
		// Isolate entry accounting from byte charging and fence packing, as in
		// testRetirementReserveBoundaries's expanded-byte boundary fixture.
		fenceBytes:  1,
		packedAt:    1,
		packedTable: []byte{1},
	}
	for i := range maxFenceEntries - 2 {
		s.retired[strconv.Itoa(i)] = 1
	}
	// Recovery has no admission high-water threshold, but must still reserve
	// one fence per active generation and attempt at the exact hard ceiling.
	before := CapacityGauge{ProjectedBytes: maxSnapshotBytes}
	if err := s.PreflightRecovery(before); err != nil {
		t.Fatal("retirement reserve refused exact entry ceiling", err)
	}
	s.landings["second"] = Landing{AttemptID: "second"}
	if err := s.PreflightRecovery(before); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "queue retirement reserve") {
		t.Fatal("retirement reserve admitted one entry beyond ceiling", err)
	}
}

func TestRecoveryPreflightKeepsHardCapsAndReduction(t *testing.T) {
	cases := map[string]State{
		"unchanged":        {bytes: 100},
		"growth":           {bytes: 101},
		"fence bytes":      {fenceBytes: maxFenceTableBytes + 1},
		"projection bytes": {bytes: maxSnapshotBytes/2 + 1},
		"records":          {records: make([]Record, MaxActiveRecords+1)},
	}
	pending := State{landings: map[string]Landing{}}
	for i := range MaxPendingLandings + 1 {
		key := fmt.Sprint(i)
		pending.landings[key] = Landing{Stage: "tested", AttemptID: key}
	}
	cases["attempts"] = pending
	for name, s := range cases {
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

// Exercise the exact hard boundary without repeatedly decoding 128 MiB of
// escaped keys. A small real checkpoint independently proves the transfer from
// active records to retired fences in short mode; full mode retains the stress.
func testRetirementReserveBoundaries(t *testing.T) {
	t.Helper()
	h, _ := fixture(t)
	h.Request.Repository = strings.Repeat("\x01", 256)
	h.Request.Queue = strings.Repeat("\x02", 256)
	h.Request.Generation = strings.Repeat("\x03", 256)
	s := State{groups: map[string]group{}, landings: map[string]Landing{}}
	reserve := 0
	for i := range 500 {
		h.Request.ChangeID = strings.Repeat("\x04", 249) + fmt.Sprintf("%07d", i)
		key, id := generationKey(h.Request), uint64(i+1)
		s.groups[key] = group{ids: []uint64{id}}
		reserve += fenceSize(key, id)
	}
	for i := range MaxPendingLandings {
		attempt := strings.Repeat("\x05", 249) + fmt.Sprintf("%07d", i)
		s.landings[attempt] = Landing{Stage: "tested", AttemptID: attempt}
		reserve += fenceSize(attempt, ^uint64(0))
	}
	for _, delta := range []int{-1, 0, 1} {
		s.fenceBytes = maxFenceTableBytes - reserve + delta
		// Isolate expanded capacity from compression and projection thresholds.
		s.packedAt, s.packedTable = s.fenceBytes, []byte{1}
		for _, recovery := range []bool{false, true} {
			err := s.Preflight(99)
			if recovery {
				err = s.PreflightRecovery(CapacityGauge{ProjectedBytes: maxSnapshotBytes})
			}
			if (err != nil) != (delta > 0) {
				t.Fatalf("recovery=%v, retirement bytes=cap%+d: %v", recovery, delta, err)
			}
		}
	}
	s = State{}
	for i := range 32 {
		h.Request.ChangeID = fmt.Sprint("escaped-", i)
		h.Digest = InventoryDigest(h, h.Commits)
		s = apply(t, s, uint64(i+1), h)
	}
	s = retainedSnapshot(t, s)
	if err := s.Preflight(99); err != nil {
		t.Fatal(err)
	}
	s = RetireOwned(s, s.lastID+1, "", func(p.InventoryRequest) bool { return true })
	s = retainedSnapshot(t, s)
	if len(s.groups) != 0 || len(s.retired) != 32 {
		t.Fatal("retirement work count differs")
	}
}
