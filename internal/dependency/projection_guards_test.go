package dependency

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestReplayStateVerifyBinding(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	if s.VerifyBinding(f.init) != nil || (State{}).VerifyBinding(f.init) == nil || s.VerifyBinding(JournalInitialized{}) == nil {
		t.Fatal("full replay failed to enforce expected initialization")
	}
	other := f.init
	other.Installation = "456"
	if s.VerifyBinding(other) == nil {
		t.Fatal("foreign replay binding accepted")
	}
}

func TestApplyStaleJournalIDDoesNotRegress(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	d, _ := s.Declaration(f.alias)
	s, err := Apply(s, 10, f.policy(true, "author"))
	if err != nil {
		t.Fatal(err)
	}
	o := Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: d.Revision, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("e", 40), Result: "waiting"}
	p := f.policy(false, "author")
	p.PriorEventID = 10
	for _, event := range []Event{o, p} {
		for _, id := range []uint64{9, 10} {
			next, err := Apply(s, id, event)
			if !errors.Is(err, ErrStale) || next.lastID != 10 || !bytes.Equal(snapshotBytes(t, s), snapshotBytes(t, next)) {
				t.Fatalf("stale %T at %d regressed state: last=%d err=%v", event, id, next.lastID, err)
			}
		}
	}
}

func TestPolicyPriorEventCAS(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(true, "author"))
	stale := f.policy(true, "author", "independent")
	stale.PriorEventID = s.lastID
	revoked := f.policy(false, "author", "independent")
	revoked.PriorEventID = s.lastID
	s = applyJournal(t, s, revoked)
	before := snapshotBytes(t, s)
	next, err := Apply(s, s.lastID+1, stale)
	if !errors.Is(err, ErrStale) || !bytes.Equal(before, snapshotBytes(t, next)) {
		t.Fatal("stale policy overwrote revocation", err)
	}
}

func TestRetargetRevokesSelfAckWithoutNewLoosening(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(true, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	s = applyJournal(t, s, f.declaration(t, s))
	s = applyJournal(t, s, selfAck(ackFor(t, s, f.alias, "author")))
	p := f.policy(false, "author", "independent")
	p.Queue.Base = "release"
	s = applyJournal(t, s, p)
	retarget, _ := s.Declaration(f.alias)
	retarget.ChangeID = f.ref(9, 0).ChangeID
	retarget.Queue.Base = "release"
	retarget.Prior = s.Prior(f.alias)
	retarget.Poll++
	retarget.Hold = s.declarationHold(retarget)
	if !retarget.Hold.Held {
		t.Fatal("retarget without opt-in did not restore hold")
	}
	s = applyJournal(t, s, retarget)
	if h := s.Hold(f.alias); !h.Held || !h.AcknowledgementRevoked {
		t.Fatal(h)
	}
}

func TestNoMultiWriterWarningWithoutOptIn(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	if s.Hold(f.alias).MultiWriterWarning {
		t.Fatal("warning without opt-in")
	}
	p := f.policy(true, "author", "independent")
	p.PriorEventID = s.policy(mustDeclaration(t, s, f.alias)).EventID
	s = applyJournal(t, s, p)
	if !s.Hold(f.alias).MultiWriterWarning {
		t.Fatal("missing opted-in warning")
	}
	p.Policy.SingleMaintainer = false
	p.Digest = PolicyDigest(p.Policy, p.Enabled)
	p.PriorEventID = s.lastID
	s = applyJournal(t, s, p)
	if s.Hold(f.alias).MultiWriterWarning {
		t.Fatal("warning after opt-in revoked")
	}
}

func mustDeclaration(t testing.TB, s State, alias SourceAlias) DependenciesDeclared {
	t.Helper()
	d, ok := s.Declaration(alias)
	if !ok {
		t.Fatal("missing declaration")
	}
	return d
}

func TestNewLooseningResetsEditorsAfterIndependentAck(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	first := f.declaration(t, s, Input{Reference: f.ref(1, 0)})
	first.Attribution.Editors = []string{"old-editor"}
	first.Hold = s.declarationHold(first)
	s = applyJournal(t, s, first)
	s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
	next := f.declaration(t, s)
	next.Attribution.Editors = []string{"new-editor"}
	next.Hold = s.declarationHold(next)
	s = applyJournal(t, s, next)
	if !slices.Equal(s.Hold(f.alias).ExcludedEditors, []string{"new-editor"}) {
		t.Fatal(s.Hold(f.alias))
	}
	// The previous editor is eligible on the new edit; the new editor is not.
	if _, err := Apply(s, s.lastID+1, ackFor(t, s, f.alias, "new-editor")); !errors.Is(err, ErrHeld) {
		t.Fatal(err)
	}
	s = applyJournal(t, s, ackFor(t, s, f.alias, "old-editor"))
	assertHeld(t, s, f.alias, false)
}

func TestDirectApplyRejectsForgedDeclarationBindings(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.declaration(t, s, Input{Alias: f.registry.Bindings[0].Aliases[0]}))
	for _, tc := range []struct {
		name   string
		mutate func(*DependenciesDeclared)
		err    error
	}{
		{"change-queue-migration", func(d *DependenciesDeclared) { d.Queue.Base = "release" }, ErrConflict},
		{"change-alias-migration", func(d *DependenciesDeclared) { d.Alias.Number++; d.Prior = Basis{} }, ErrConflict},
		{"alias-binding-freeze", func(d *DependenciesDeclared) {
			d.Edges[0].Reference = canonical(t, f.ref(9, 0))
			d.Bindings[0].Reference = d.Edges[0].Reference
		}, ErrStale},
		{"edge-queue-freeze", func(d *DependenciesDeclared) { d.Edges[0].Queue.Base = "release" }, ErrStale},
		{"edge-baseline-freeze", func(d *DependenciesDeclared) { d.Edges[0].Baseline.Head = strings.Repeat("e", 40) }, ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustDeclaration(t, s, f.alias)
			d.Prior = s.Prior(f.alias)
			d.Poll++
			d.Revision++
			tc.mutate(&d)
			d.Digest = DeclarationDigest(d.Edges)
			d.Hold = s.declarationHold(d)
			// These are structurally valid forged records bypassing BindDeclaration.
			if err := d.Validate(); err != nil {
				t.Fatal("invalid fixture", err)
			}
			next, err := Apply(s, s.lastID+1, d)
			if !errors.Is(err, tc.err) || !bytes.Equal(snapshotBytes(t, s), snapshotBytes(t, next)) {
				t.Fatal("forged event accepted", err)
			}
		})
	}
}

func TestAcknowledgementOnUnheldDeclarationRejected(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	if _, err := Apply(s, s.lastID+1, ackFor(t, s, f.alias, "independent")); !errors.Is(err, ErrStale) {
		t.Fatal("unheld declaration acknowledged", err)
	}
}

func TestSnapshotNegativeDeclarationBindings(t *testing.T) {
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	s = applyJournal(t, s, f.declaration(t, s))
	s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
	// A distinct alias/change makes the duplicate-ChangeID check load-bearing.
	second := mustDeclaration(t, s, f.alias)
	second.Alias.Number++
	second.ChangeID = f.ref(9, 0).ChangeID
	second.Prior = Basis{}
	second.Hold = s.declarationHold(second)
	s = applyJournal(t, s, second)
	for _, tc := range []struct {
		name   string
		mutate func(*snapshot)
	}{
		{"duplicate-change", func(v *snapshot) { v.Declarations[1].Event.ChangeID = v.Declarations[0].Event.ChangeID }},
		{"ack-basis", func(v *snapshot) { v.Declarations[0].Ack.Event.Declaration.Digest = strings.Repeat("c", 64) }},
		{"declaration-order", func(v *snapshot) { slices.Reverse(v.Declarations) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v snapshot
			if err := json.Unmarshal(snapshotBytes(t, s), &v); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&v)
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := DecodeSnapshot(raw, f.init); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	// Observation basis corruption must remain structurally valid and point to
	// a real edge, so no unrelated decoder/edge guard can kill the mutation.
	s = f.start(t)
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	d := mustDeclaration(t, s, f.alias)
	s = applyJournal(t, s, Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: d.Revision, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("e", 40), Result: "waiting"})
	var v snapshot
	if err := json.Unmarshal(snapshotBytes(t, s), &v); err != nil {
		t.Fatal(err)
	}
	v.Observations[0].Event.Declaration.Digest = strings.Repeat("c", 64)
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeSnapshot(raw, f.init); err == nil {
		t.Fatal("observation basis accepted")
	}
}

func TestTwoAliasSnapshotSort(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.declaration(t, s))
	d := mustDeclaration(t, s, f.alias)
	d.Alias.Number++
	d.ChangeID = f.ref(9, 0).ChangeID
	s = applyJournal(t, s, d)
	// Repeated serialization detects nondeterministic map order when sorting is
	// removed. Two aliases (rather than the old one-entry fixture) are essential.
	for range 128 {
		var v snapshot
		if err := json.Unmarshal(snapshotBytes(t, s), &v); err != nil {
			t.Fatal(err)
		}
		if len(v.Declarations) != 2 || v.Declarations[0].EventID >= v.Declarations[1].EventID {
			t.Fatal("unsorted declarations")
		}
	}
}
