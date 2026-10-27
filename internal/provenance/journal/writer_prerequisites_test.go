package journal

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// #186: retire through the landings branch, rather than compact's successful
// tested/landed pair path. A fresh inventory isolates the attempt-reuse guard.
func TestRetireOwnedLandingsBranchSettlesAttempt(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	s = apply(t, s, 11, l)
	s = RetireOwned(s, 12, l.BatchID, nil)
	if s.settled[l.AttemptID] != 11 || len(s.landings) != 0 {
		t.Fatal("terminal outcome did not settle the tested attempt")
	}
	s = retainedSnapshot(t, s)
	h.Request.Generation, h.PreviousObservationID = "fresh-after-terminal", 3
	h.Digest = InventoryDigest(h, h.Commits)
	s = apply(t, s, 13, h)
	l = landing(h, []uint64{13})
	rejectUnchanged(t, s, 20, l, ErrBinding)
}

func TestFenceTableLifetimeLandings(t *testing.T) {
	n := 200001
	if testing.Short() {
		n = 400
	}
	h, _ := fixture(t)
	h.Request.Repository = "https://github.com/CanadaApollo6/openmerge"
	var s State
	var first Observation
	var firstAttempt string
	for i := 0; i < n; i++ {
		id := uint64(i*4 + 3)
		// Random-looking, non-monotonic identities exercise realistic entropy,
		// distinct changes, and 64-byte generations, rather than decimal ranges.
		h.Request.ChangeID = fmt.Sprintf("chg1_%x", sha256.Sum256(fmt.Appendf(nil, "change-%d", i)))
		h.Request.Generation = fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "generation-%d", i)))
		h.PreviousObservationID = 0
		h.Digest = InventoryDigest(h, h.Commits)
		var err error
		s, err = ApplyOwned(s, id, h)
		if err != nil {
			t.Fatal(i, err)
		}
		l := landing(h, []uint64{id})
		l.DecisionID = id - 1
		l.AttemptID = fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "attempt-%d", i)))[:32]
		if i == 0 {
			first, firstAttempt = h, l.AttemptID
		}
		s, err = ApplyOwned(s, id+1, l)
		if err != nil {
			t.Fatal(i, err)
		}
		l.Stage = "landed"
		l.Members[0].Mapping.Build.Stage = "landed"
		l.Members[0].Mapping.Build.ID = "composition-L"
		l.Members[0].RecordDigest = MemberDigest(l.Members[0])
		s, err = ApplyOwned(s, id+2, l)
		if err != nil {
			t.Fatal(i, err)
		}
		if len(s.records) != 0 {
			t.Fatal("completed payloads accumulated")
		}
	}
	if len(s.retired) != n || len(s.settled) != n {
		t.Fatal("lost exact fences")
	}
	// Geometric capacity measurements must visit O(N) entries in both modes.
	if s.packingWork > uint64(8*n) {
		t.Fatalf("nonlinear packing: %d visits for %d landings", s.packingWork, n)
	}
	if err := s.Preflight(); err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 24<<20 {
		t.Fatalf("insufficient headroom: %d", len(raw))
	}
	if !bytes.Contains(raw, []byte(`"FenceTable"`)) {
		t.Fatal("table compaction not exercised")
	}
	t.Logf("%d landings: %d checkpoint bytes, %d raw fence bytes, %d capacity visits", n, len(raw), s.fenceBytes, s.packingWork)
	restored := retainedSnapshot(t, s)
	if len(restored.retired) != n || len(restored.settled) != n {
		t.Fatal("reload lost fences")
	}
	id := uint64(n*4 + 3)
	// Supply the current CAS anchor so a missing retired-ID guard cannot be
	// masked by an unrelated predecessor mismatch.
	first.PreviousObservationID = 3
	first.Digest = InventoryDigest(first, first.Commits)
	rejectUnchanged(t, restored, id, first, ErrBinding)
	// Use a new, valid inventory so only the settled attempt guard can reject.
	first.Request.Generation = "fresh-after-compaction"
	first.PreviousObservationID = 3
	first.Digest = InventoryDigest(first, first.Commits)
	restored, err = ApplyOwned(restored, id, first)
	if err != nil {
		t.Fatal(err)
	}
	l := landing(first, []uint64{id})
	l.AttemptID = firstAttempt
	rejectUnchanged(t, restored, id+1, l, ErrBinding)
	// A resumed suffix must produce exactly the same checkpoint as full replay.
	full, err := ApplyOwned(s, id, first)
	if err != nil {
		t.Fatal(err)
	}
	a, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), full)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), restored)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("snapshot/tail diverged", err)
	}
}

func TestFenceTableRejectsCorruptionAndReadsOldCheckpoint(t *testing.T) {
	c := checkpoint{Through: 999, Generations: []fence{}, Settled: []fence{}}
	for i := 0; i < 128; i++ {
		c.Settled = append(c.Settled, fence{fmt.Sprintf("attempt-%03d", i), uint64(i + 1)})
	}
	old, err := json.Marshal(compactSnapshot{9, "queue", json.RawMessage(`{"Version":6}`), 999, []Record{}, c})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnapshot(old, func(string, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	modern, err := EncodeSnapshot("queue", decoded.Base, decoded.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	var v tableSnapshot
	if err := json.Unmarshal(modern, &v); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*tableSnapshot){
		"truncated":     func(v *tableSnapshot) { v.FenceTable = v.FenceTable[:len(v.FenceTable)/2] },
		"trailing":      func(v *tableSnapshot) { v.FenceTable = v.FenceTable + "AA==" },
		"future anchor": func(v *tableSnapshot) { v.LastID = 1 },
		"duplicate anchor": func(v *tableSnapshot) {
			bad := c
			bad.Settled = append([]fence{}, c.Settled...)
			bad.Settled[1].ID = bad.Settled[0].ID
			v.FenceTable = base64.StdEncoding.EncodeToString(packFences(bad))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var next tableSnapshot
			if err := json.Unmarshal(modern, &next); err != nil {
				t.Fatal(err)
			}
			mutate(&next)
			b, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = DecodeSnapshot(b, func(string, []byte) error { return nil }); err == nil {
				t.Fatal("accepted corrupt table")
			}
		})
	}
	// The compressed representation must still enforce anchor reuse semantics.
	if decoded.Provenance.settled["attempt-000"] != 1 {
		t.Fatal("old checkpoint fence lost")
	}
	_, err = unpackFences([]byte("invalid"), 999)
	if !errors.Is(err, ErrMalformed) {
		t.Fatal(err)
	}
}

func TestFenceTableArbitraryIdentities(t *testing.T) {
	c := checkpoint{Through: 999, Generations: []fence{}, Settled: []fence{}}
	for i, key := range []string{"00", "0123456789abcdef0", "0123456789abcdef01", "AABBCCDDEEFF001122", "abc-0123456789abcdef0123-quoted-\"-\\", "not-monotonic", "é任意的标识"} {
		c.Settled = append(c.Settled, fence{key, uint64(i + 1)})
	}
	raw := packFences(c)
	got, err := unpackFences(raw, c.Through)
	if err != nil {
		t.Fatal(err)
	}
	var alternate bytes.Buffer
	w, err := zlib.NewWriterLevel(&alternate, zlib.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(encodeFenceTable(c)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unpackFences(alternate.Bytes(), c.Through); err != nil {
		t.Fatal("valid alternate compressor rejected", err)
	}

	a, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("arbitrary identities changed", err)
	}
}

func TestFenceTableExpansionBound(t *testing.T) {
	var raw bytes.Buffer
	w := zlib.NewWriter(&raw)
	payload := binary.AppendUvarint([]byte{1}, maxFenceEntries+1)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unpackFences(raw.Bytes(), 1); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded fence allocation", err)
	}
	s := State{fenceBytes: maxFenceTableBytes + 1}
	if err := s.Preflight(); !errors.Is(err, ErrLimit) {
		t.Fatal("writer admits unreadable expansion", err)
	}
	if _, err := EncodeSnapshot("queue", []byte(`{"Version":6}`), s); !errors.Is(err, ErrLimit) {
		t.Fatal("encoder admits unreadable expansion", err)
	}
}

func TestFenceTablePreservesLongEscapedGenerationKeys(t *testing.T) {
	// Each request field admits 256 bytes. JSON escaping of control characters
	// can make the canonical generation key exceed four KiB without violating
	// the existing event contract. Compaction must not narrow that contract.
	keys := map[string]uint64{}
	for i := 0; i < 128; i++ {
		parts := []string{strings.Repeat("\x01", 256), strings.Repeat("\x02", 256), strings.Repeat("\x03", 256), strings.Repeat("\x04", 250) + fmt.Sprintf("%03d", i)}
		keys[stringJSON(parts)] = uint64(i + 1)
	}
	c := checkpoint{Through: 128, Generations: sortedFences(keys), Settled: []fence{}}
	s, err := restoreCheckpoint(c, []Record{})
	if err != nil {
		t.Fatal(err)
	}
	s = retainedSnapshot(t, s)
	if len(s.retired) != 128 {
		t.Fatal("long generation identities lost")
	}
}
