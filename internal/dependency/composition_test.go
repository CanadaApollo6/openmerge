package dependency

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"reflect"
	"testing"
	"time"
)

func TestCompositionCompanionStrictCodecAndSemanticBasis(t *testing.T) {
	f, s, o, d := decisionFixture(t)
	m, err := s.CompositionMember(d.ChangeID, d.Revision, d.Head)
	if err != nil {
		t.Fatal(err)
	}
	c := CompositionRecorded{Namespace: f.init.Namespace, BatchID: "b1", StartEventID: 100, BaseSHA: d.TestedSHA, Members: []CompositionMember{m}}
	kind, raw, err := EncodeEvent(c)
	if err != nil || kind != "dependency_composition_recorded" {
		t.Fatal(kind, err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(c, decoded) {
		t.Fatal("codec", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	for _, path := range jsonFields(tree, nil) {
		for _, mode := range []string{"missing", "unknown", "null", "case"} {
			if _, err := DecodeEvent(mutateField(t, raw, path, mode)); err == nil {
				t.Fatal("accepted malformed companion", path, mode)
			}
		}
	}
	composed, err := Apply(s, 101, c)
	if err != nil {
		t.Fatal(err)
	}
	if !composed.CompositionValid(c) {
		t.Fatal("fresh basis invalid")
	}
	// Refreshing observation IDs, read sequences and equal policy identity is not
	// a different tested input, and must not spend another CI run.
	o.ExpectedPrior = s.lastID
	o.ReadSequence++
	refreshed := applyJournal(t, composed, o)
	if !refreshed.CompositionValid(c) {
		t.Fatal("same evidence caused rebuild")
	}
	revised := o
	revised.ExpectedPrior = refreshed.lastID
	revised.ReadSequence++
	revised.SourceRevision.Revision++
	if applyJournal(t, refreshed, revised).CompositionValid(c) {
		t.Fatal("different satisfying landing retained composition")
	}

	o.ExpectedPrior = refreshed.lastID
	o.ReadSequence++
	o.Result = "waiting"
	waiting := applyJournal(t, refreshed, o)
	if waiting.CompositionValid(c) {
		t.Fatal("rewound source retained composition")
	}
	p := f.policy(false)
	p.PriorEventID = 2
	p.Policy.AllowFrom = []string{}
	p.Digest = PolicyDigest(p.Policy, true)
	if applyJournal(t, composed, p).CompositionValid(c) {
		t.Fatal("policy edit retained composition")
	}
	if applyJournal(t, composed, f.declaration(t, composed)).CompositionValid(c) {
		t.Fatal("declaration edit retained composition")
	}
	stale := cloneComposition(c)
	stale.Members[0].Admission.ObservationIDs[0]--
	if _, err := Apply(s, s.lastID+1, stale); !errors.Is(err, ErrStale) {
		t.Fatal("stale audit input admitted", err)
	}
	// Accessors and event inputs cannot mutate the active projection.
	detached, _, _ := composed.Composition("b1")
	detached.Members[0].Admission.ObservationIDs[0]++
	actual, _, _ := composed.Composition("b1")
	if !reflect.DeepEqual(actual, c) {
		t.Fatal("accessor alias")
	}
	retired := composed.RetainCompositions(nil)
	if _, _, ok := retired.Composition("b1"); ok {
		t.Fatal("terminal retained composition")
	}
	if _, _, ok := composed.Composition("b1"); !ok {
		t.Fatal("retirement mutated prior state")
	}
}

func TestCompositionMaximumBatchFitsEventCap(t *testing.T) {
	f, s, _, d := decisionFixture(t)
	m, err := s.CompositionMember(d.ChangeID, d.Revision, d.Head)
	if err != nil {
		t.Fatal(err)
	}
	m.Admission.ObservationIDs = []uint64{}
	for id := uint64(1); id <= MaxDirectEdges; id++ {
		m.Admission.ObservationIDs = append(m.Admission.ObservationIDs, id)
	}
	c := CompositionRecorded{Namespace: f.init.Namespace, BatchID: "maximum", StartEventID: 100, BaseSHA: d.TestedSHA, Members: []CompositionMember{}}
	for i := range 128 {
		member := m
		member.ChangeID = fmt.Sprintf("chg1_%064x", i+1)
		member.Alias.Number = uint64(i + 1)
		c.Members = append(c.Members, member)
	}
	_, raw, err := EncodeEvent(c)
	if err != nil || len(raw) >= maxEventBytes {
		t.Fatal("maximum composition exceeds event cap", len(raw), err)
	}
}

func TestCompositionSnapshotRejectsForgedWrapperAndFutureCause(t *testing.T) {
	f, s, _, d := decisionFixture(t)
	m, err := s.CompositionMember(d.ChangeID, d.Revision, d.Head)
	if err != nil {
		t.Fatal(err)
	}
	c := CompositionRecorded{Namespace: f.init.Namespace, BatchID: "b1", StartEventID: 100, BaseSHA: d.TestedSHA, Members: []CompositionMember{m}}
	s, err = Apply(s, 101, c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	q := queue.State{BaseBranch: "main", BaseSHA: d.TestedSHA}
	for _, event := range []queue.Event{queue.PRObserved{PR: 2, HeadSHA: m.Head, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 2, At: now}, queue.BatchStarted{BatchID: "b1", BaseSHA: d.TestedSHA, PRs: []queue.BatchPR{{PR: 2, HeadSHA: m.Head}}, At: now}} {
		q = queue.Apply(q, event)
	}
	raw, err := EncodeSnapshot(q, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeSnapshot(raw, f.init); err != nil {
		t.Fatal("valid composition snapshot", err)
	}
	legacy, err := queue.EncodeSnapshot(q)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*compositionSnapshot){
		"legacy base":              func(v *compositionSnapshot) { v.Base = legacy },
		"future cause":             func(v *compositionSnapshot) { v.Compositions[0].InvalidatedBy = v.LastID + 1 },
		"cause before composition": func(v *compositionSnapshot) { v.Compositions[0].InvalidatedBy = 1 },
		"wrong member":             func(v *compositionSnapshot) { v.Compositions[0].Event.Members[0].Alias.Number++ },
	} {
		var v compositionSnapshot
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		mutate(&v)
		bad, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeSnapshot(bad, f.init); err == nil {
			t.Fatal("accepted forged wrapper", name)
		}
	}
}
