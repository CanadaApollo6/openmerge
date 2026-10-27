package journalreader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func provenanceFixture(t testing.TB) pj.Observation {
	t.Helper()
	raw, err := os.ReadFile("../provenance/journal/testdata/complete.json")
	must(t, err)
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	h := e.(pj.Observation)
	h.Request.ChangeID = "chg1_" + strings.Repeat("1", 64)
	h.Digest = pj.InventoryDigest(h, h.Commits)
	return h
}
func encode(t testing.TB, e any) []byte {
	t.Helper()
	var raw []byte
	var err error
	switch v := e.(type) {
	case pj.Event:
		_, raw, err = pj.EncodeEvent(v)
	case dependency.Event:
		_, raw, err = dependency.EncodeEvent(v)
	case queue.Event:
		_, raw, err = queue.EncodeEvent(v)
	default:
		t.Fatalf("unknown event %T", e)
	}
	must(t, err)
	return raw
}
func landingFixture(t testing.TB, h pj.Observation) pj.Landing {
	t.Helper()
	raw, err := os.ReadFile("../provenance/journal/testdata/tested.json")
	must(t, err)
	e, err := pj.DecodeEvent(raw)
	must(t, err)
	l := e.(pj.Landing)
	m := &l.Members[0]
	m.Request, m.Mapping.Request = h.Request, h.Request
	m.InventoryDigest = h.Digest
	m.ObservationIDs = []uint64{5, 7}
	for i := range m.Layers {
		m.Layers[i].References = []uint64{10}
	}
	m.RecordDigest = pj.MemberDigest(*m)
	return l
}
func decisionFixture(h pj.Observation) queue.LandingDecisionRecorded {
	return queue.LandingDecisionRecorded{BatchID: "batch-1", AttemptID: "attempt-1", TestedSHA: h.Request.Head, LandedSHA: h.Request.Head, Variant: "generated_remedy", Remedy: &queue.GeneratedRemedyDecision{ChangeID: queue.ChangeID(h.Request.ChangeID), Revision: 1, Head: h.Request.Head, Suspect: h.Request.Head, Evidence: []string{"synthetic-fixture"}, OnRed: "revert"}}
}
func snapshot(t testing.TB, r Reader) []byte {
	t.Helper()
	base, err := dependency.EncodeSnapshot(r.Queue, r.dependencies)
	must(t, err)
	records := r.Provenance.Records()
	if len(records) == 0 {
		return base
	}
	kind := "queue"
	var header struct{ Version int }
	must(t, json.Unmarshal(base, &header))
	if header.Version == 8 {
		kind = "dependency"
	}
	raw, err := json.Marshal(struct {
		Version  int
		BaseKind string
		Base     json.RawMessage
		LastID   uint64
		Records  []pj.Record
	}{9, kind, base, records[len(records)-1].ID, records})
	must(t, err)
	return raw
}
func lookup(records map[uint64][]byte) Lookup {
	return func(id uint64) ([]byte, error) {
		raw, ok := records[id]
		if !ok {
			return nil, fmt.Errorf("missing %d", id)
		}
		return raw, nil
	}
}
func apply(t testing.TB, r *Reader, id uint64, raw []byte) {
	t.Helper()
	_, err := r.Apply(id, raw)
	must(t, err)
}
func identity(t testing.TB) dependency.JournalInitialized {
	t.Helper()
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	must(t, err)
	return init
}

func TestMixedSnapshotTailEveryCut(t *testing.T) {
	h := provenanceFixture(t)
	a, b := h, h
	a.Chunks, b.Chunks, b.Index = 2, 2, 1
	a.Commits, b.Commits = h.Commits[:1], h.Commits[1:]
	l := landingFixture(t, h)
	landed := l
	landed.Stage = "landed"
	landed.Members = append([]pj.Member{}, l.Members...)
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].Mapping.Build.ID = "composition-L"
	landed.Members[0].RecordDigest = pj.MemberDigest(landed.Members[0])
	init := identity(t)
	request := dependencytest.Request()
	observation := dependency.Observed{Namespace: init.Namespace, Alias: request.Consumer.Alias, ChangeID: request.Consumer.ChangeID, Revision: 1, Declaration: dependency.Basis{EventID: 4, Digest: request.Consumer.Digest}, Source: request.Edge.Reference, ReadSequence: 1, SourceRevision: request.Source.Current, SourceBase: request.Source.BaseSHA, Result: "waiting"}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	events := []struct {
		id uint64
		e  any
	}{
		{1, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}},
		{2, init}, {3, queue.EnqueueRequested{PR: 2, At: at}}, {4, request.Consumer},
		{5, a}, {6, observation}, {7, b}, {8, queue.PRMetadataObserved{PR: 2, Title: "held", ApprovedReviewsBy: []string{}, Labels: []string{}, Files: []string{}}},
		{10, decisionFixture(h)}, {11, l}, {12, queue.BaseMoved{SHA: "base"}}, {13, landed},
	}
	records := map[uint64][]byte{}
	for _, e := range events {
		records[e.id] = encode(t, e.e)
	}
	full := New(queue.State{}, init, lookup(records))
	for _, e := range events {
		apply(t, &full, e.id, records[e.id])
	}
	want := snapshot(t, full)
	for cut := 0; cut <= len(events); cut++ {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			prefix := New(queue.State{}, init, lookup(records))
			var through uint64
			for _, e := range events[:cut] {
				apply(t, &prefix, e.id, records[e.id])
				through = e.id
			}
			raw := snapshot(t, prefix)
			resumed := New(queue.State{}, init, lookup(records))
			must(t, resumed.LoadSnapshot(raw, through))
			for _, e := range events[cut:] {
				apply(t, &resumed, e.id, records[e.id])
			}
			if !bytes.Equal(want, snapshot(t, resumed)) {
				t.Fatal("snapshot/tail diverged")
			}
			if !reflect.DeepEqual(queue.Plan(queue.Config{}, full.Queue, at), queue.Plan(queue.Config{}, resumed.Queue, at)) {
				t.Fatal("planner diverged")
			}
			// #163 held request survives both nested wrappers and partial chunks.
			qraw, err := queue.EncodeSnapshot(resumed.Queue)
			must(t, err)
			if !bytes.Contains(qraw, []byte(`"PendingConditions":"sign-off"`)) || !bytes.Contains(qraw, []byte(`"HeldRequests":{"2":`)) {
				t.Fatalf("held request lost: %s", qraw)
			}
			if cut > 1 {
				if _, err := queue.DecodeSnapshot(raw); err == nil {
					t.Fatal("old queue decoder accepted new snapshot")
				}
			}
		})
	}
}

func TestReferencesFailClosedFullReplayAndSnapshot(t *testing.T) {
	h := provenanceFixture(t)
	// Single observation at 5 for this focused fixture.
	l := landingFixture(t, h)
	l.Members[0].ObservationIDs = []uint64{5}
	l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
	goodDecision := decisionFixture(h)
	for name, mutate := range map[string]func(map[uint64][]byte, *pj.Landing){
		"missing decision":    func(m map[uint64][]byte, _ *pj.Landing) { delete(m, 10) },
		"wrong decision kind": func(m map[uint64][]byte, _ *pj.Landing) { m[10] = encode(t, queue.BaseMoved{SHA: "base"}) },
		"wrong batch":         func(m map[uint64][]byte, _ *pj.Landing) { d := goodDecision; d.BatchID = "other"; m[10] = encode(t, d) },
		"wrong attempt": func(m map[uint64][]byte, _ *pj.Landing) {
			d := goodDecision
			d.AttemptID = "other"
			m[10] = encode(t, d)
		},
		"wrong member": func(m map[uint64][]byte, _ *pj.Landing) {
			d := goodDecision
			remedy := *d.Remedy
			remedy.Head = strings.Repeat("f", 40)
			d.Remedy = &remedy
			m[10] = encode(t, d)
		},
		"missing layer": func(_ map[uint64][]byte, l *pj.Landing) { l.Members[0].Layers[0].References = []uint64{9} },
		"wrong layer kind": func(m map[uint64][]byte, l *pj.Landing) {
			m[9] = encode(t, queue.BaseMoved{SHA: "base"})
			l.Members[0].Layers[0].References = []uint64{9}
		},
		"self layer": func(_ map[uint64][]byte, l *pj.Landing) { l.Members[0].Layers[0].References = []uint64{11} },
	} {
		t.Run(name, func(t *testing.T) {
			local := l
			local.Members = append([]pj.Member{}, l.Members...)
			local.Members[0].Layers = append([]pj.Layer{}, l.Members[0].Layers...)
			records := map[uint64][]byte{5: encode(t, h), 10: encode(t, goodDecision)}
			mutate(records, &local)
			local.Members[0].RecordDigest = pj.MemberDigest(local.Members[0])
			r := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
			apply(t, &r, 5, records[5])
			before := snapshot(t, r)
			_, err := r.Apply(11, encode(t, local))
			if !errors.Is(err, ErrReference) {
				t.Fatalf("accepted invalid references: %v", err)
			}
			if !bytes.Equal(before, snapshot(t, r)) {
				t.Fatal("failed apply mutated state")
			}
			// The pure fold only fences external references. Test-only v9 construction
			// must not bypass resolution in the composed snapshot reader.
			pure, err := pj.Apply(r.Provenance, 11, local)
			if err == nil {
				r.Provenance = pure
				restored := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
				if err := restored.LoadSnapshot(snapshot(t, r), 11); !errors.Is(err, ErrReference) {
					t.Fatalf("snapshot bypassed resolution: %v", err)
				}
			}
		})
	}
}

func TestDependencyBindingQuarantineAndMismatch(t *testing.T) {
	init := identity(t)
	r := New(queue.State{}, dependency.JournalInitialized{}, nil)
	apply(t, &r, 1, encode(t, init))
	if _, err := r.Dependencies(); err == nil {
		t.Fatal("unverified initialization became usable")
	}
	raw := snapshot(t, r)
	unbound := New(queue.State{}, dependency.JournalInitialized{}, nil)
	must(t, unbound.LoadSnapshot(raw, 1))
	if _, err := unbound.Dependencies(); err == nil {
		t.Fatal("snapshot trusted its own identity")
	}
	bound := New(queue.State{}, init, nil)
	must(t, bound.LoadSnapshot(raw, 1))
	if _, err := bound.Dependencies(); err != nil {
		t.Fatal(err)
	}
	other := init
	other.Incarnation = strings.Repeat("b", 64)
	mismatch := New(queue.State{}, other, nil)
	if _, err := mismatch.Apply(1, encode(t, init)); err == nil {
		t.Fatal("accepted rotated identity")
	}
	if err := mismatch.LoadSnapshot(raw, 1); err == nil {
		t.Fatal("snapshot accepted rotated identity")
	}
	if err := bound.LoadSnapshot(raw, 0); err == nil {
		t.Fatal("snapshot crossed cursor watermark")
	}
}

func TestLegacyMainSnapshotAndPlannerBytes(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	r := New(queue.State{}, dependency.JournalInitialized{}, nil)
	apply(t, &r, 1, encode(t, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}))
	apply(t, &r, 2, encode(t, queue.EnqueueRequested{PR: 2, At: at}))
	want, err := os.ReadFile("../dependency/testdata/legacy-held.snapshot.json")
	must(t, err)
	if !bytes.Equal(want, snapshot(t, r)) {
		t.Fatal("origin/main golden changed")
	}
	restored := New(queue.State{}, dependency.JournalInitialized{}, nil)
	must(t, restored.LoadSnapshot(want, 2))
	if !bytes.Equal(want, snapshot(t, restored)) {
		t.Fatal("legacy snapshot changed on loading")
	}
	if !reflect.DeepEqual(queue.Plan(queue.Config{}, r.Queue, at), queue.Plan(queue.Config{}, restored.Queue, at)) {
		t.Fatal("legacy plan changed")
	}
}

func TestMultiMemberDecisionsResolveIndependently(t *testing.T) {
	h := provenanceFixture(t)
	other := h
	other.Request.ChangeID = "chg1_" + strings.Repeat("2", 64)
	other.Digest = pj.InventoryDigest(other, other.Commits)
	l := landingFixture(t, h)
	l.Members[0].ObservationIDs = []uint64{5}
	l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
	second := l.Members[0]
	second.Request, second.Mapping.Request = other.Request, other.Request
	second.InventoryDigest = other.Digest
	second.ObservationIDs = []uint64{6}
	second.Layers = append([]pj.Layer{}, second.Layers...)
	for i := range second.Layers {
		second.Layers[i].References = []uint64{9}
	}
	second.RecordDigest = pj.MemberDigest(second)
	l.Members = append(l.Members, second)
	records := map[uint64][]byte{5: encode(t, h), 6: encode(t, other), 9: encode(t, decisionFixture(other)), 10: encode(t, decisionFixture(h))}
	r := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
	apply(t, &r, 5, records[5])
	apply(t, &r, 6, records[6])
	before := r
	apply(t, &r, 11, encode(t, l))
	restored := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
	must(t, restored.LoadSnapshot(snapshot(t, r), 11))
	if !bytes.Equal(snapshot(t, r), snapshot(t, restored)) {
		t.Fatal("multi-member snapshot changed")
	}
	// Every referenced target still exists and has the right kind, but belongs
	// to the first member. It cannot certify the second member's evidence.
	records[9] = records[10]
	if _, err := before.Apply(11, encode(t, l)); !errors.Is(err, ErrReference) {
		t.Fatal("cross-member decision accepted", err)
	}
}

func TestMemberBatchIdentitiesReplayAndV9(t *testing.T) {
	for _, scenario := range []struct {
		name                  string
		tested, landed, check string
		reject                bool
	}{
		{name: "per-member intermediate build", check: "anchor"},
		{name: "conflicting tested", tested: strings.Repeat("e", 40), reject: true},
		{name: "conflicting landed", landed: strings.Repeat("e", 40), reject: true},
		{name: "check at intermediate SHA", check: "intermediate", reject: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h := provenanceFixture(t)
			other := h
			other.Request.ChangeID = "chg1_" + strings.Repeat("2", 64)
			other.Digest = pj.InventoryDigest(other, other.Commits)
			l := landingFixture(t, h)
			l.Members[0].ObservationIDs = []uint64{5}
			l.Members[0].RecordDigest = pj.MemberDigest(l.Members[0])
			second := l.Members[0]
			second.Request, second.Mapping.Request = other.Request, other.Request
			second.InventoryDigest, second.ObservationIDs = other.Digest, []uint64{6}
			// Intermediate per-member composition results need not equal the final
			// attempt's tested/landed SHA, even when their decisions agree on it.
			second.Mapping.Build.Result = strings.Repeat("e", 40)
			second.Layers = append([]pj.Layer{}, second.Layers...)
			for i := range second.Layers {
				second.Layers[i].References = []uint64{9}
				if scenario.check != "" {
					second.Layers[i].References = []uint64{8, 9}
				}
			}
			second.RecordDigest = pj.MemberDigest(second)
			l.Members = append(l.Members, second)
			d := decisionFixture(other)
			if scenario.tested != "" {
				d.TestedSHA = scenario.tested
			}
			if scenario.landed != "" {
				d.LandedSHA = scenario.landed
			}
			checkSHA := h.Request.Head
			if scenario.check == "intermediate" {
				checkSHA = second.Mapping.Build.Result
			}
			records := map[uint64][]byte{5: encode(t, h), 6: encode(t, other), 8: encode(t, queue.ChecksCompleted{TestedSHA: checkSHA, Success: true}), 9: encode(t, d), 10: encode(t, decisionFixture(h))}
			r := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
			for _, id := range []uint64{5, 6, 8, 9, 10} {
				apply(t, &r, id, records[id])
			}
			before := snapshot(t, r)
			_, err := r.Apply(11, encode(t, l))
			if scenario.reject {
				if !errors.Is(err, ErrReference) {
					t.Fatalf("accepted contradictory member evidence: %v", err)
				}
				if !bytes.Equal(before, snapshot(t, r)) {
					t.Fatal("rejection mutated state")
				}
			} else {
				must(t, err)
			}
			// Assemble through the pure fold, so v9 validation independently proves
			// the reference constraint rather than inheriting successful replay.
			pure, err := pj.Apply(pj.State{}, 5, h)
			must(t, err)
			pure, err = pj.Apply(pure, 6, other)
			must(t, err)
			r.Provenance, err = pj.Apply(pure, 11, l)
			must(t, err)
			restored := New(queue.State{}, dependency.JournalInitialized{}, lookup(records))
			err = restored.LoadSnapshot(snapshot(t, r), 11)
			if scenario.reject {
				if !errors.Is(err, ErrReference) {
					t.Fatalf("v9 accepted contradictory member evidence: %v", err)
				}
			} else {
				must(t, err)
			}
		})
	}
}
