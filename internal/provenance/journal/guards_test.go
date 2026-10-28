package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
)

func rejectUnchanged(t *testing.T, s State, id uint64, e Event, want error) {
	t.Helper()
	before := snapshotBytes(t, []byte(`{"Version":6}`), s)
	next, err := Apply(s, id, e)
	if !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
	if !bytes.Equal(before, snapshotBytes(t, []byte(`{"Version":6}`), next)) {
		t.Fatal("rejection changed retained state")
	}
}

func TestCompleteInventoryRejectsForgedDeclarationDigest(t *testing.T) {
	h, c := fixture(t)
	h.DeclarationDigest = strings.Repeat("a", 64)
	h.Digest = InventoryDigest(h, c)
	// Both digests have valid shape and the inventory digest correctly hashes the
	// forged declaration field. Only recomputation from declarations detects it.
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	rejectUnchanged(t, State{}, 3, h, ErrBinding)
}

func TestPassingMemberRequiresCompleteMapping(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	m := &l.Members[0]
	m.Mapping.Complete = false
	m.Mapping.Diagnostic = p.Diagnostic{Outcome: p.ReadPending, Resource: "mapping"}
	m.RecordDigest = MemberDigest(*m)
	rejectUnchanged(t, apply(t, State{}, 3, h), 11, l, ErrMalformed)
	// A pending member can retain an incomplete mapping.
	m.Result, m.Layers[0].Truth = "pending", "pending"
	m.RecordDigest = MemberDigest(*m)
	apply(t, apply(t, State{}, 3, h), 11, l)
}

func TestDuplicateLandingAttemptStageRejected(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	s := apply(t, apply(t, State{}, 3, h), 11, l)
	rejectUnchanged(t, s, 12, l, ErrBinding)
	l.AttemptID = "another-attempt"
	apply(t, s, 12, l)
}

func TestLandingDecisionMustPrecedeEvent(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	for _, decision := range []uint64{11, 12} {
		t.Run(fmt.Sprint(decision), func(t *testing.T) {
			l := landing(h, []uint64{3})
			l.DecisionID = decision
			rejectUnchanged(t, s, 11, l, ErrBinding)
		})
	}
	apply(t, s, 11, landing(h, []uint64{3}))
}

func TestCompleteDiagnosticRequiresReadComplete(t *testing.T) {
	for _, outcome := range []p.ReadOutcome{p.ReadPending, p.LimitExceeded, p.InvalidInput} {
		t.Run(string(outcome), func(t *testing.T) {
			h, _ := fixture(t)
			// Empty resource/object isolate the outcome guard from the other checks.
			h.Diagnostic = p.Diagnostic{Outcome: outcome}
			h.Digest = InventoryDigest(h, h.Commits)
			rejectUnchanged(t, State{}, 3, h, ErrMalformed)
		})
	}
}

func TestLandingRequiresCompleteInventoryIndependently(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	// Isolate the completeness fence from the declaration-digest binding. Wire
	// validation makes incomplete headers carry an empty declaration digest, so
	// simply replaying one would let that second fence mask removal of this one.
	g := s.groups[generationKey(h.Request)]
	g.header.Complete = false
	s.groups[generationKey(h.Request)] = g
	rejectUnchanged(t, s, 11, landing(h, []uint64{3}), ErrIncomplete)
}

func TestSupersededGenerationRejectsLateChunk(t *testing.T) {
	h, c := fixture(t)
	cc := chunks(h, c)
	s := apply(t, State{}, 3, cc[0])
	newer := h
	newer.Request.Generation = "new-generation"
	newer.PreviousObservationID = 3
	newer.Digest = InventoryDigest(newer, c)
	s = apply(t, s, 4, newer)
	// The old header, index, assembled content and digest all still match.
	rejectUnchanged(t, s, 5, cc[1], ErrBinding)
}

func TestAssembledInventoryDigestRejectsCorruption(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint(chunked), func(t *testing.T) {
			h, c := fixture(t)
			h.Digest = strings.Repeat("0", 64)
			if !chunked {
				rejectUnchanged(t, State{}, 3, h, ErrBinding)
				return
			}
			// Both chunks carry the same corrupted digest: header equality passes.
			cc := chunks(h, c)
			s := apply(t, State{}, 3, cc[0])
			rejectUnchanged(t, s, 5, cc[1], ErrBinding)
		})
	}
}

func TestLandedRequiresPriorTestedAttempt(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	landed := landing(h, []uint64{3})
	landed.Stage = "landed"
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].Mapping.Build.ID = "composition-L"
	landed.Members[0].RecordDigest = MemberDigest(landed.Members[0])
	rejectUnchanged(t, s, 13, landed, ErrBinding)
	other := landing(h, []uint64{3})
	other.AttemptID = "another-attempt"
	rejectUnchanged(t, apply(t, s, 11, other), 13, landed, ErrBinding)
	apply(t, apply(t, s, 11, landing(h, []uint64{3})), 13, landed)
}

func TestSnapshotRejectsNoncanonicalPayloads(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	canonical := s.Records()[0].Payload
	raw := snapshotBytes(t, []byte(`{"Version":6}`), s)
	for name, payload := range map[string][]byte{
		"whitespace":  bytes.Replace(canonical, []byte(`{"version":1,`), []byte(`{ "version":1,`), 1),
		"field order": bytes.Replace(canonical, []byte(`{"version":1,"type":"change_provenance_observed",`), []byte(`{"type":"change_provenance_observed","version":1,`), 1),
		"escape":      bytes.Replace(canonical, []byte(`"change-1"`), []byte(`"\u0063hange-1"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(payload, canonical) {
				t.Fatal("mutation did not change bytes")
			}
			if _, err := DecodeEvent(payload); err != nil {
				t.Fatal("fixture must be valid noncanonical JSON", err)
			}
			bad := bytes.Replace(raw, canonical, payload, 1)
			if _, err := DecodeSnapshot(bad, func(string, []byte) error { return nil }); !errors.Is(err, ErrMalformed) {
				t.Fatal("accepted noncanonical retained payload", err)
			}
		})
	}
}

func TestSnapshotBaseKindVersionBindings(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	for _, kind := range []string{"queue", "dependency", "future", ""} {
		for version := 1; version <= 10; version++ {
			t.Run(fmt.Sprintf("%s/%d", kind, version), func(t *testing.T) {
				v := snapshot{SnapshotVersion, kind, json.RawMessage(fmt.Sprintf(`{"Version":%d}`, version)), 3, s.Records()}
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				called := false
				decoded, err := DecodeSnapshot(raw, func(gotKind string, _ []byte) error {
					called = true
					if gotKind != kind {
						t.Fatalf("callback kind: got %q, want %q", gotKind, kind)
					}
					return nil
				})
				want := kind == "queue" && version >= 2 && version <= 7 || kind == "dependency" && version == 8
				if (err == nil) != want || called != want {
					t.Fatalf("kind/version callback fence: want %v, called %v, err %v", want, called, err)
				}
				if want && decoded.BaseKind != kind {
					t.Fatal("returned wrong base kind")
				}
			})
		}
	}
}

func TestUnwrappedSnapshotBaseKinds(t *testing.T) {
	for version := 2; version <= 8; version++ {
		want := "queue"
		if version == 8 {
			want = "dependency"
		}
		raw := []byte(fmt.Sprintf(`{"Version":%d}`, version))
		decoded, err := DecodeSnapshot(raw, func(kind string, base []byte) error {
			if kind != want || !bytes.Equal(base, raw) {
				t.Fatalf("unwrapped v%d: kind %q, base %s", version, kind, base)
			}
			return nil
		})
		if err != nil || decoded.BaseKind != want {
			t.Fatalf("unwrapped v%d: %v", version, err)
		}
	}
}

func TestSnapshotBaseBytesDetached(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			base := []byte(`{"Version":6}`)
			raw := bytes.Clone(base)
			if wrapped {
				h, _ := fixture(t)
				raw = snapshotBytes(t, base, apply(t, State{}, 3, h))
			}
			original := bytes.Clone(raw)
			decoded, err := DecodeSnapshot(raw, func(_ string, b []byte) error {
				b[0] = '!'
				return nil
			})
			if err != nil || !bytes.Equal(raw, original) || !bytes.Equal(decoded.Base, base) {
				t.Fatal("base decoder aliased input or output", err)
			}
			decoded.Base[0] = '!'
			if !bytes.Equal(raw, original) {
				t.Fatal("returned base aliased caller bytes")
			}
		})
	}
}

func TestRetainedRecordCap(t *testing.T) {
	if maxRecords != MaxPendingLandings*(MaxBatchMembers+1)+MaxBatchMembers {
		t.Fatalf("record contract changed: %d", maxRecords)
	}
	if testing.Short() {
		testRetainedRecordCapShort(t)
		return
	}
	h, _ := fixture(t)
	// Empty pending observations keep this focused on record count, not bytes.
	h.Complete, h.DeclarationDigest = false, ""
	h.Diagnostic = p.Diagnostic{Outcome: p.ReadPending, Resource: "git"}
	h.Stable, h.Additional, h.Commits = []string{}, []string{}, []p.Commit{}
	s := capacityFixture(t)
	for i := 1; i <= maxRecords; i++ {
		h.Request.ChangeID = fmt.Sprint(i)
		h.PreviousObservationID = 0
		h.Request.Generation = fmt.Sprint(i)
		h.Digest = InventoryDigest(h, h.Commits)
		s = addCapacityInventory(t, s, uint64(i+2), h)
		if i == maxRecords-1 {
			if err := s.Preflight(); err != nil {
				t.Fatal("preflight refused headroom", err)
			}
		}
	}
	if err := s.Preflight(); !errors.Is(err, ErrLimit) {
		t.Fatal("writer preflight must stay below cap", err)
	}
	if len(s.Records()) != maxRecords {
		t.Fatal("lost retained records")
	}
	h.Request.ChangeID = "overflow"
	h.PreviousObservationID = 0
	h.Request.Generation = "overflow"
	h.Digest = InventoryDigest(h, h.Commits)
	rejectUnchanged(t, s, maxRecords+3, h, ErrLimit)
	_, limitErr := Apply(s, maxRecords+3, h)
	var limit RetentionLimitError
	if !errors.As(limitErr, &limit) || limit.Records != maxRecords+1 || !strings.Contains(limitErr.Error(), "shared-reader-retention-recovery") {
		t.Fatal("missing typed operator recovery", limitErr)
	}
	// Also pin the wrapper's early count limit separately from Apply's cap.
	raw := snapshotBytes(t, []byte(`{"Version":6}`), s)
	decoded, err := DecodeSnapshot(raw, func(string, []byte) error { return nil })
	if err != nil || len(decoded.Provenance.Records()) != maxRecords {
		t.Fatal("snapshot at the record ceiling rejected", err)
	}
	var v snapshot
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	_, payload, err := EncodeEvent(h)
	if err != nil {
		t.Fatal(err)
	}
	v.Records = append(v.Records, Record{maxRecords + 3, payload})
	v.LastID++
	raw, err = json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw, func(string, []byte) error { return nil }); !errors.Is(err, ErrLimit) {
		t.Fatal("snapshot record cap not enforced before fold", err)
	}
}

func TestSnapshotReaderRecordCap(t *testing.T) {
	// Stop after the count guard, before record replay. The reduced checkpoint
	// in testRetainedRecordCapShort independently exercises the retained fold.
	stop := errors.New("base decoder reached")
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("cap%+d", delta), func(t *testing.T) {
			count := maxRecords + delta
			v := snapshot{Version: SnapshotVersion, BaseKind: "queue", Base: []byte(`{"Version":6}`), LastID: uint64(count), Records: make([]Record, count)}
			for i := range v.Records {
				v.Records[i] = Record{ID: uint64(i + 1), Payload: json.RawMessage(`{}`)}
			}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = DecodeSnapshot(raw, func(string, []byte) error {
				called = true
				return stop
			})
			if delta <= 0 {
				if !called || !errors.Is(err, stop) {
					t.Fatalf("reader refused %d records before base decoding: %v", count, err)
				}
			} else {
				var limit RetentionLimitError
				if called || !errors.As(err, &limit) || limit.Records != count {
					t.Fatalf("reader must reject %d records before base decoding: %v", count, err)
				}
			}
		})
	}
}

func TestLandingDeclarationDigestBoundToInventory(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	l := landing(h, []uint64{3})
	l.Members[0].DeclarationDigest = strings.Repeat("f", 64)
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	rejectUnchanged(t, s, 11, l, ErrBinding)
}

func TestTestedLandedBatchMismatch(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	s := apply(t, apply(t, State{}, 3, h), 11, l)
	l.Stage, l.BatchID = "landed", "other-batch"
	l.Members[0].Mapping.Build.Stage = "landed"
	l.Members[0].Mapping.Build.ID = "composition-L"
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	rejectUnchanged(t, s, 13, l, ErrBinding)
}

func TestInventoryUnionCoversEveryOriginal(t *testing.T) {
	h, c := fixture(t)
	h.Stable = []string{}
	h.Digest = InventoryDigest(h, c)
	rejectUnchanged(t, State{}, 3, h, ErrBinding)
}

func TestMappingCannotMapOriginalToMarker(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	l.Members[0].Mapping.Build.Marker = l.Members[0].Mapping.Commits[0].Build
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	rejectUnchanged(t, apply(t, State{}, 3, h), 11, l, ErrBinding)
}

func TestLayerReferenceEqualToOwnPosition(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	l.Members[0].Layers[0].References = []uint64{11}
	l.Members[0].RecordDigest = MemberDigest(l.Members[0])
	rejectUnchanged(t, apply(t, State{}, 3, h), 11, l, ErrBinding)
}

func TestAssembledCommitSortOrder(t *testing.T) {
	h, c := fixture(t)
	c[0], c[1] = c[1], c[0]
	h.Digest = InventoryDigest(h, c)
	cc := chunks(h, c) // Each chunk is sorted; only the assembled-order guard fires.
	rejectUnchanged(t, apply(t, State{}, 3, cc[0]), 5, cc[1], ErrBinding)
}

func TestChunkCommitSortOrder(t *testing.T) {
	h, c := fixture(t)
	c[0], c[1] = c[1], c[0]
	h.Digest = InventoryDigest(h, c)
	rejectUnchanged(t, State{}, 3, h, ErrMalformed)
}

func testRetainedRecordCapShort(t *testing.T) {
	t.Helper()
	h, _ := fixture(t)
	s := State{}
	for i := range 32 {
		h.Request.ChangeID = fmt.Sprint(i)
		h.Digest = InventoryDigest(h, h.Commits)
		s = apply(t, s, uint64(i+1), h)
	}
	s = retainedSnapshot(t, s)
	if len(s.records) != 32 {
		t.Fatal("reduced checkpoint lost records")
	}
	// Record admission is independent of payload contents. Pad only the count
	// model; the real reduced fixture above exercises strict checkpoint replay.
	s.records = append(s.records, make([]Record, maxRecords-len(s.records)-1)...)
	if err := s.Preflight(); err != nil {
		t.Fatal("preflight refused headroom", err)
	}
	s.records = append(s.records, Record{})
	if err := s.Preflight(); !errors.Is(err, ErrLimit) {
		t.Fatal("writer preflight must stay below cap", err)
	}
	if err := s.capacity(false); err != nil {
		t.Fatal("hard cap refused ceiling", err)
	}
	s.records = append(s.records, Record{})
	err := s.capacity(false)
	var limit RetentionLimitError
	if !errors.As(err, &limit) || limit.Records != maxRecords+1 || !strings.Contains(err.Error(), "shared-reader-retention-recovery") {
		t.Fatal("missing typed operator recovery at record ceiling", err)
	}
	_, payload, err := EncodeEvent(h)
	if err != nil {
		t.Fatal(err)
	}
	v := snapshot{Version: 9, BaseKind: "queue", Base: []byte(`{"Version":6}`), LastID: maxRecords + 1}
	for i := range maxRecords + 1 {
		v.Records = append(v.Records, Record{uint64(i + 1), payload})
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSnapshot(raw, func(string, []byte) error { return nil }); !errors.Is(err, ErrLimit) {
		t.Fatal("snapshot record cap not enforced before fold", err)
	}
}
