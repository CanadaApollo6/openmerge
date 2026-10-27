package dependency

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type journalFixture struct {
	init     JournalInitialized
	registry Registry
	alias    SourceAlias
}

func journalTestFixture(t testing.TB) journalFixture {
	t.Helper()
	var token [32]byte
	token[0] = 1
	init, err := NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "123")
	if err != nil {
		t.Fatal(err)
	}
	registry := registryFixture()
	registry.Scope = init.scope()
	for i := range registry.Bindings {
		registry.Bindings[i].Reference.Scope = init.scope()
	}
	return journalFixture{init, registry, SourceAlias{init.ForgeInstance, init.Installation, "repo-client-17", 2}}
}
func (f journalFixture) ref(n int, revision uint64) Reference {
	r := testRef(n, revision)
	r.Scope = f.init.scope()
	return r
}
func (f journalFixture) baseline(n int) Baseline {
	return Baseline{7, fmt.Sprintf("%064x", n), fmt.Sprintf("%040x", n)}
}
func (f journalFixture) grant(who string) Grant {
	return Grant{who, who, f.alias.RepositoryID, "fresh-permission-read", true, true}
}
func applyJournal(t testing.TB, s State, event Event) State {
	t.Helper()
	next, err := Apply(s, s.lastID+1, event)
	if err != nil {
		t.Fatalf("apply %T: %v", event, err)
	}
	return next
}
func (f journalFixture) start(t testing.TB) State { return applyJournal(t, State{}, f.init) }
func (f journalFixture) declaration(t testing.TB, s State, inputs ...Input) DependenciesDeclared {
	t.Helper()
	old, _ := s.Declaration(f.alias)
	read := DependenciesDeclared{Alias: f.alias, ChangeID: f.ref(3, 0).ChangeID, Revision: 1, Queue: QueueKey{f.registry.Repositories[1], "main", ""}, Poll: old.Poll + 1, Attribution: Attribution{[]string{"author"}, false}, ReadGrant: f.grant("author"), Accountability: Accountability{"author", "submitter", []string{"human"}}}
	baselines := map[string]Baseline{}
	for _, r := range []Reference{f.ref(1, 0), f.ref(1, 7), f.ref(2, 0)} {
		raw, _ := r.Canonical()
		baselines[raw] = f.baseline(1)
	}
	e, err := BindDeclaration(s, read, inputs, f.registry, baselines)
	if err != nil || e == nil {
		t.Fatalf("bind: %v %+v", err, e)
	}
	return *e
}
func (f journalFixture) policy(enabled bool, writers ...string) PolicyApplied {
	p := Policy{Mode: "ordered", AllowFrom: []string{f.registry.Repositories[0]}, AllowTo: []string{}, SingleMaintainer: enabled}
	grants := []Grant{}
	for _, w := range writers {
		grants = append(grants, f.grant(w))
	}
	return PolicyApplied{Enabled: true, Namespace: f.init.Namespace, Alias: f.alias, Queue: QueueKey{f.registry.Repositories[1], "main", ""}, DefaultBranch: "main", ReadRef: "refs/heads/main", ReadID: "default-branch-config-read", Commit: strings.Repeat("b", 40), Digest: PolicyDigest(p, true), Policy: p, Writers: grants}
}
func ackFor(t testing.TB, s State, alias SourceAlias, who string) Acknowledged {
	t.Helper()
	d, ok := s.declarations[alias.key()]
	if !ok {
		t.Fatal("missing declaration")
	}
	p := s.policy(d.Event)
	return Acknowledged{s.initialization.Namespace, alias, s.Prior(alias), Grant{who, who, alias.RepositoryID, "fresh-writer-read", true, true}, exclusions(d), p.EventID, p.Event.Commit, p.Event.Digest}
}
func selfAck(a Acknowledged) SelfAcknowledged { return SelfAcknowledged{a, slices.Clone(a.Exclusions)} }
func assertHeld(t testing.TB, s State, alias SourceAlias, want bool) {
	t.Helper()
	if s.Hold(alias).Held != want {
		t.Fatalf("hold %+v; want %v", s.Hold(alias), want)
	}
}
func snapshotBytes(t testing.TB, s State) []byte {
	t.Helper()
	raw, err := EncodeSnapshot(queue.State{}, s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJournalNamespaceGoldenAndRecovery(t *testing.T) {
	f := journalTestFixture(t)
	if f.init.Namespace != "omj1_eed77a52f64dc38383f87f9c7b9fe5d5bfb1c18b6e415a8d140f8277b2bfe245" {
		t.Fatalf("namespace %s", f.init.Namespace)
	}
	a := f.start(t)
	a = applyJournal(t, a, f.declaration(t, a, Input{Alias: f.registry.Bindings[0].Aliases[0]}))
	raw := snapshotBytes(t, a)
	for _, reason := range []string{"divergent history", "restore", "physical clone rotation", "reinitialization", "schema recreation"} {
		t.Run(reason, func(t *testing.T) {
			var token [32]byte
			token[0] = 2
			identity := f.init.DatabaseIdentity
			if reason == "schema recreation" {
				identity = "openmerge-db-v2:sha256:" + strings.Repeat("b", 64)
			}
			rotated, err := NewJournalIdentity(identity, token, f.init.ForgeInstance, f.init.Installation)
			if err != nil {
				t.Fatal(err)
			}
			other := f
			other.init = rotated
			other.registry.Scope = rotated.scope()
			other.registry.Bindings = slices.Clone(f.registry.Bindings)
			for i := range other.registry.Bindings {
				other.registry.Bindings[i].Reference.Scope = rotated.scope()
			}
			b := other.start(t)
			e := other.declaration(t, b, Input{Reference: other.ref(1, 0)})
			e.Edges[0].Baseline.Head = strings.Repeat("c", 40)
			b = applyJournal(t, b, e)
			// Same consumer ID/revision and source ID/revision, different histories.
			if a.Prior(f.alias).EventID != b.Prior(f.alias).EventID {
				t.Fatal("fixture IDs differ")
			}
			if _, err := ParseReference(canonical(t, f.ref(1, 0)), rotated.scope()); !errors.Is(err, ErrOutOfScope) {
				t.Fatal(err)
			}
			if _, _, err := DecodeSnapshot(raw, rotated); !errors.Is(err, ErrOutOfScope) {
				t.Fatal("accepted old snapshot", err)
			}
			if _, err := Apply(a, a.lastID+1, rotated); err == nil {
				t.Fatal("reinitialized live journal")
			}
			empty := f.declaration(t, a)
			if _, err := Apply(b, b.lastID+1, empty); !errors.Is(err, ErrOutOfScope) {
				t.Fatal("accepted foreign empty declaration", err)
			}
		})
	}
	if _, _, err := DecodeSnapshot(raw, JournalInitialized{}); err == nil {
		t.Fatal("missing binding accepted")
	}
	if err := f.init.VerifyBinding(f.init); err != nil {
		t.Fatal("restart changed binding", err)
	}
	bad := f.init
	bad.DatabaseIdentity = ""
	if bad.Validate() == nil {
		t.Fatal("missing database identity accepted")
	}
	if _, err := NewJournalIdentity(f.init.DatabaseIdentity, [32]byte{}, f.init.ForgeInstance, f.init.Installation); err == nil {
		t.Fatal("zero incarnation accepted")
	}
}

func TestDeclarationHoldStrictWriterAuthorAndRetarget(t *testing.T) {
	for _, writers := range [][]string{{"author"}, {"author", "independent"}} {
		t.Run(strings.Join(writers, "+"), func(t *testing.T) {
			f := journalTestFixture(t)
			s := f.start(t)
			s = applyJournal(t, s, f.policy(false, writers...))
			s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
			assertHeld(t, s, f.alias, false)
			prior := s
			removal := f.declaration(t, s)
			s = applyJournal(t, s, removal)
			assertHeld(t, s, f.alias, true)
			assertHeld(t, prior, f.alias, false)
			for _, who := range []string{"author", "submitter", "human"} {
				a := ackFor(t, s, f.alias, who)
				if _, err := Apply(s, s.lastID+1, a); !errors.Is(err, ErrHeld) {
					t.Fatal("excluded writer acknowledged", who, err)
				}
				if _, err := Apply(s, s.lastID+1, selfAck(a)); err == nil {
					t.Fatal("implicit single-maintainer exception", who)
				}
			}
			// Retarget to another enrolled base creates a new change at the same head.
			// The prior declaration is empty and still durable; it cannot be omitted.
			retarget := removal
			retarget.ChangeID = f.ref(4, 0).ChangeID
			retarget.Queue.Base = "release"
			retarget.Poll++
			retarget.Prior = s.Prior(f.alias)
			stale := retarget
			stale.Prior = Basis{}
			if _, err := Apply(s, s.lastID+1, stale); !errors.Is(err, ErrStale) {
				t.Fatal("reset alias prior", err)
			}
			oldAck := ackFor(t, s, f.alias, "independent")
			p := f.policy(false, writers...)
			p.Queue.Base = "release"
			s = applyJournal(t, s, p)
			s = applyJournal(t, s, retarget)
			assertHeld(t, s, f.alias, true)
			if s.Prior(f.alias).Digest != removal.Digest {
				t.Fatal("retarget lost declaration")
			}
			if _, err := Apply(s, s.lastID+1, oldAck); err == nil {
				t.Fatal("old action cleared retarget hold")
			}

			// Renaming/transferring the repository preserves the stable source alias.
			renamed := retarget
			renamed.ChangeID = f.ref(5, 0).ChangeID
			renamed.Queue.Repository = "https://github.com/renamed/client"
			renamed.Prior = s.Prior(f.alias)
			renamed.Poll++
			s = applyJournal(t, s, renamed)
			assertHeld(t, s, f.alias, true)
			s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
			assertHeld(t, s, f.alias, false)
			replacement := removal
			replacement.Alias.Number++
			replacement.Prior = Basis{}
			replacement.ChangeID = f.ref(6, 0).ChangeID
			replacement.Hold = s.declarationHold(replacement)
			s = applyJournal(t, s, replacement)
			assertHeld(t, s, replacement.Alias, false)
		})
	}
}

func TestTighteningDigestABAAndFreshGrants(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.policy(false, "author"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	assertHeld(t, s, f.alias, true)
	oldAck := ackFor(t, s, f.alias, "independent")
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	assertHeld(t, s, f.alias, true)
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	assertHeld(t, s, f.alias, true)
	if s.Prior(f.alias).Digest != oldAck.Declaration.Digest {
		t.Fatal("fixture did not restore digest")
	}
	if _, err := Apply(s, s.lastID+1, oldAck); !errors.Is(err, ErrStale) {
		t.Fatal("digest ABA accepted", err)
	}
	for _, mutate := range []func(*Acknowledged){func(a *Acknowledged) { a.Writer.Fresh = false }, func(a *Acknowledged) { a.Writer.Allowed = false }, func(a *Acknowledged) { a.Writer.ReturnedID = "other" }, func(a *Acknowledged) { a.Writer.RepositoryID = "foreign" }, func(a *Acknowledged) { a.Exclusions = []string{} }} {
		a := ackFor(t, s, f.alias, "independent")
		mutate(&a)
		if _, err := Apply(s, s.lastID+1, a); err == nil {
			t.Fatal("bad writer/exclusions accepted")
		}
	}
	s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
	assertHeld(t, s, f.alias, false)
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	assertHeld(t, s, f.alias, false)
}

func TestCoalescedEditorsUnknownAndAuthenticatedRedeclaration(t *testing.T) {
	f := journalTestFixture(t)
	for _, unknown := range []bool{false, true} {
		s := f.start(t)
		s = applyJournal(t, s, f.policy(true, "author", "independent"))
		s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
		removal := f.declaration(t, s)
		removal.Attribution = CoalesceEditors(Attribution{[]string{"author"}, false}, Attribution{[]string{"independent"}, unknown})
		removal.Hold = s.declarationHold(removal)
		s = applyJournal(t, s, removal)
		h := s.Hold(f.alias)
		if !slices.Equal(h.ExcludedEditors, []string{"author", "independent"}) || h.UnknownEditor != unknown {
			t.Fatal(h)
		}
		if _, err := Apply(s, s.lastID+1, ackFor(t, s, f.alias, "independent")); !errors.Is(err, ErrHeld) {
			t.Fatal("forgot earlier editor", err)
		}
		if unknown {
			for _, who := range []string{"author", "independent", "new-writer"} {
				a := ackFor(t, s, f.alias, who)
				var event Event = a
				if who != "new-writer" {
					event = selfAck(a)
				}
				if _, err := Apply(s, s.lastID+1, event); !errors.Is(err, ErrHeld) {
					t.Fatal("unknown edit acknowledged", err)
				}
			}
			bad := removal
			bad.Prior = s.Prior(f.alias)
			bad.Poll++
			bad.Redeclaration = true
			if _, err := Apply(s, s.lastID+1, bad); err == nil {
				t.Fatal("unauthenticated redeclaration accepted")
			}
			good := bad
			good.Attribution = Attribution{[]string{"author"}, false}
			good.Hold = s.declarationHold(good)
			s = applyJournal(t, s, good)
			if s.Hold(f.alias).UnknownEditor || !slices.Equal(s.Hold(f.alias).ExcludedEditors, []string{"author", "independent"}) {
				t.Fatal("attribution reset exclusions")
			}
			assertHeld(t, s, f.alias, true)
		}
		// No eligible writer leaves the hold indefinitely; adding a fresh writer recovers.
		s = applyJournal(t, s, ackFor(t, s, f.alias, "new-writer"))
		assertHeld(t, s, f.alias, false)
	}
	if !CoalesceEditors(Attribution{Editors: []string{}}).Unknown {
		t.Fatal("polled body invented editor")
	}
}

func TestSingleMaintainerGoverningPolicyAndRevocation(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			f := journalTestFixture(t)
			s := f.start(t)
			writers := []string{"author"}
			if multi {
				writers = append(writers, "independent")
			}
			p := f.policy(true, writers...)
			s = applyJournal(t, s, p)
			policyID := s.lastID
			s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
			s = applyJournal(t, s, f.declaration(t, s))
			before := s
			ack := selfAck(ackFor(t, s, f.alias, "author"))
			kind, raw, err := EncodeEvent(ack)
			if err != nil || kind != "dependency_declaration_self_acknowledged" {
				t.Fatal(kind, err)
			}
			decoded, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			s = applyJournal(t, s, decoded)
			assertHeld(t, s, f.alias, false)
			assertHeld(t, before, f.alias, true)
			status := s.Hold(f.alias)
			if status.LastAcknowledgementKind != kind || status.MultiWriterWarning != multi {
				t.Fatal(status)
			}
			// Audit evidence survives an opted-in -> revoked -> opted-in policy sequence.
			p = f.policy(false, writers...)
			p.PriorEventID = policyID
			s = applyJournal(t, s, p)
			policyID = s.lastID
			assertHeld(t, s, f.alias, true)
			if !s.Hold(f.alias).AcknowledgementRevoked {
				t.Fatal("lost audited revocation")
			}
			_, restored, err := DecodeSnapshot(snapshotBytes(t, s), f.init)
			if err != nil {
				t.Fatal(err)
			}
			s = restored
			p = f.policy(true, writers...)
			p.PriorEventID = policyID
			s = applyJournal(t, s, p)
			assertHeld(t, s, f.alias, true)
			if _, err := Apply(s, s.lastID+1, ack); err == nil {
				t.Fatal("replayed pre-revocation self ack")
			}
			s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
			assertHeld(t, s, f.alias, false)
		})
	}
	f := journalTestFixture(t)
	s := f.start(t)
	head := f.policy(true, "author")
	head.ReadRef = "refs/pull/2/head"
	if _, err := Apply(s, s.lastID+1, head); err == nil {
		t.Fatal("head-only opt-in accepted")
	}
	s = applyJournal(t, s, f.policy(false, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	s = applyJournal(t, s, f.declaration(t, s))
	if _, err := Apply(s, s.lastID+1, selfAck(ackFor(t, s, f.alias, "author"))); err == nil {
		t.Fatal("head proposal authorized itself")
	}
	// A's opt-in cannot authorize B's self-acknowledgement.
	source := f.policy(true, "author")
	source.Alias.RepositoryID = "repo-api"
	source.Queue.Repository = f.registry.Repositories[0]
	source.Writers = []Grant{}
	s = applyJournal(t, s, source)
	if _, err := Apply(s, s.lastID+1, selfAck(ackFor(t, s, f.alias, "author"))); err == nil {
		t.Fatal("source policy authorized consumer")
	}
}

func TestBindingPollRacesBaselinesAndImmutableDedupe(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	input := Input{Alias: f.registry.Bindings[0].Aliases[0]}
	first := f.declaration(t, s, input)
	competing := cloneDeclaration(declarationProjection{Event: first}).Event
	s = applyJournal(t, s, first)
	if _, err := Apply(s, s.lastID+1, competing); !errors.Is(err, ErrStale) {
		t.Fatal("alias race accepted", err)
	}
	before := snapshotBytes(t, s)
	replay, err := Apply(s, s.lastID, first)
	if err != nil || !bytes.Equal(before, snapshotBytes(t, replay)) {
		t.Fatal("retry not idempotent", err)
	}
	first.Edges[0].Baseline.Head = strings.Repeat("f", 40)
	first.Attribution.Editors[0] = "mutated"
	if !bytes.Equal(before, snapshotBytes(t, s)) {
		t.Fatal("event slices mutated state")
	}
	changed := competing
	changed.Poll++
	if _, err := Apply(s, s.lastID, changed); !errors.Is(err, ErrStale) {
		t.Fatal("changed duplicate ID accepted", err)
	}
	view, _ := s.Declaration(f.alias)
	view.Edges[0].Baseline.Head = "mutated"
	view.Attribution.Editors[0] = "mutated"
	if !bytes.Equal(before, snapshotBytes(t, s)) {
		t.Fatal("accessor mutated state")
	}
	// Source pushes/retargets change the registry, not a retained alias or baseline.
	current, _ := s.Declaration(f.alias)
	current.Poll++
	current.Revision++
	moved := f.registry
	moved.Bindings = slices.Clone(moved.Bindings)
	moved.Bindings[0].Reference = f.ref(9, 0)
	moved.Bindings[0].Queue.Base = "new-base"
	declaration, err := BindDeclaration(s, current, []Input{input}, moved, map[string]Baseline{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(declaration.Edges, competing.Edges) || declaration.Digest != competing.Digest {
		t.Fatal("alias followed registry/source push")
	}
	prior := s
	s = applyJournal(t, s, *declaration)
	if !bytes.Equal(before, snapshotBytes(t, prior)) {
		t.Fatal("fold mutated earlier maps")
	}
	// Unrelated body edit coalesces to no event; two edits in one poll cannot append twice.
	current, _ = s.Declaration(f.alias)
	current.Poll++
	unchanged, err := BindDeclaration(s, current, []Input{input}, moved, nil)
	if err != nil || unchanged != nil {
		t.Fatal("unrelated edit recorded", err)
	}
	removal := f.declaration(t, s)
	s = applyJournal(t, s, removal)
	duplicatePoll := removal
	duplicatePoll.Prior = s.Prior(f.alias)
	duplicatePoll.Redeclaration = true
	if _, err := Apply(s, s.lastID+1, duplicatePoll); !errors.Is(err, ErrStale) {
		t.Fatal("two observations in poll", err)
	}
	// Separate removal/re-add gets a new binding, even if the textual alias is identical.
	current = removal
	current.Poll++
	current.Revision++
	raw := canonical(t, f.ref(9, 0))
	rebound, err := BindDeclaration(s, current, []Input{input}, moved, map[string]Baseline{raw: f.baseline(9)})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.Edges[0].Reference != raw {
		t.Fatal("separate declaration retained removed binding")
	}
	s = applyJournal(t, s, *rebound)
	assertHeld(t, s, f.alias, true)
}

func TestObservationRacesAndRevisionlessDigest(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
	declaration, _ := s.Declaration(f.alias)
	waiting := Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: declaration.ChangeID, Revision: 1, Declaration: s.Prior(f.alias), Source: declaration.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("e", 40), Result: "waiting"}
	s = applyJournal(t, s, waiting)
	before := s
	unavailable := waiting
	unavailable.ExpectedPrior = s.lastID
	unavailable.ReadSequence = 3
	unavailable.Result = "unavailable"
	s = applyJournal(t, s, unavailable)
	delayed := waiting
	delayed.ExpectedPrior = s.lastID
	delayed.ReadSequence = 2
	delayed.Result = "landed"
	if _, err := Apply(s, s.lastID+1, delayed); !errors.Is(err, ErrStale) {
		t.Fatal("delayed success cleared unavailable", err)
	}
	delayed.ReadSequence = 4
	delayed.ExpectedPrior = before.lastID
	if _, err := Apply(s, s.lastID+1, delayed); !errors.Is(err, ErrStale) {
		t.Fatal("observation CAS race accepted", err)
	}
	delayed.ExpectedPrior = s.lastID
	delayed.SourceRevision = Baseline{8, strings.Repeat("f", 64), strings.Repeat("f", 40)}
	s = applyJournal(t, s, delayed)
	after, _ := s.Declaration(f.alias)
	if !reflect.DeepEqual(declaration, after) {
		t.Fatal("source push/landing rewrote revision-less baseline/digest")
	}
	snapshot := snapshotBytes(t, s)
	_, restored, err := DecodeSnapshot(snapshot, f.init)
	if err != nil {
		t.Fatal(err)
	}
	got, _, ok := restored.Observation(delayed)
	if !ok || got.SourceRevision.Revision != 8 || got.SourceRevision.IntentDigest == declaration.Edges[0].Baseline.IntentDigest {
		t.Fatal("lost actual landed revision and intent", got)
	}
	tightened := f.declaration(t, s, Input{Reference: f.ref(1, 7)})
	s = applyJournal(t, s, tightened)
	delayed.ExpectedPrior = 0
	delayed.ReadSequence = 5
	if _, err := Apply(s, s.lastID+1, delayed); !errors.Is(err, ErrStale) {
		t.Fatal("old declaration observation accepted", err)
	}
	if _, _, ok := s.Observation(delayed); ok {
		t.Fatal("retained obsolete observation")
	}
	changed := slices.Clone(declaration.Edges)
	changed[0].Baseline = delayed.SourceRevision
	if DeclarationDigest(changed) != declaration.Digest {
		t.Fatal("baseline participates in digest")
	}
}

func TestNewKindsAreNotRuntimeQueueEvents(t *testing.T) {
	f := journalTestFixture(t)
	_, raw, err := EncodeEvent(f.init)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.DecodeEvent(raw); err == nil {
		t.Fatal("dependency decoder wired into live queue codec")
	}
	if _, ok := any(f.init).(queue.Event); ok {
		t.Fatal("dependency event implements runtime event interface")
	}
	// New history still fails old strict readers; relabeling a version cannot hide it.
	var q queue.State
	wrapped := snapshotBytes(t, f.start(t))
	if _, err := queue.DecodeSnapshot(wrapped); err == nil {
		t.Fatal("v8 wired into live snapshot decoder")
	}
	legacy, err := EncodeSnapshot(q, State{})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacy, []byte("Initialization")) {
		t.Fatal("legacy bytes gained fields")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(wrapped, &value); err != nil {
		t.Fatal(err)
	}
	value["Version"] = json.RawMessage(`6`)
	downgraded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.DecodeSnapshot(downgraded); err == nil {
		t.Fatal("snapshot version cleared rollback boundary")
	}
}

func TestDisablingDoesNotClearHoldAndAuthenticatedEditRestoresAttribution(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.policy(true, "author", "independent"))
	policyID := s.lastID
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	unknown := f.declaration(t, s, Input{Reference: f.ref(1, 0)})
	unknown.Attribution = Attribution{[]string{"editor"}, true}
	unknown.Hold = s.declarationHold(unknown)
	s = applyJournal(t, s, unknown)
	// A fresh authenticated semantic edit restores attribution without discarding
	// any editor exclusions, just as an authenticated explicit redeclaration does.
	tight := f.declaration(t, s, Input{Reference: f.ref(1, 7)})
	s = applyJournal(t, s, tight)
	if s.Hold(f.alias).UnknownEditor || !slices.Contains(s.Hold(f.alias).ExcludedEditors, "editor") {
		t.Fatal("attribution recovery erased exclusion")
	}
	assertHeld(t, s, f.alias, true)
	forged := f.declaration(t, s)
	forged.Hold = DeclarationHold{Editors: []string{}}
	if _, err := Apply(s, s.lastID+1, forged); !errors.Is(err, ErrHeld) {
		t.Fatal("event manufactured cleared hold", err)
	}
	disabled := f.policy(true, "author", "independent")
	disabled.Enabled = false
	disabled.PriorEventID = policyID
	disabled.Digest = PolicyDigest(disabled.Policy, false)
	s = applyJournal(t, s, disabled)
	assertHeld(t, s, f.alias, true)
	if _, err := Apply(s, s.lastID+1, selfAck(ackFor(t, s, f.alias, "author"))); !errors.Is(err, ErrHeld) {
		t.Fatal("disabled policy authorized self ack", err)
	}
	s = applyJournal(t, s, ackFor(t, s, f.alias, "independent"))
	assertHeld(t, s, f.alias, false)
	// Disabling an existing self-acknowledgement also restores the hold.
	enabled := f.policy(true, "author")
	enabled.PriorEventID = s.policy(tight).EventID
	s = applyJournal(t, s, enabled)
	policyID = s.lastID
	s = applyJournal(t, s, f.declaration(t, s))
	s = applyJournal(t, s, selfAck(ackFor(t, s, f.alias, "author")))
	disabled.PriorEventID = policyID
	s = applyJournal(t, s, disabled)
	assertHeld(t, s, f.alias, true)
	_, _, err := DecodeSnapshot(snapshotBytes(t, s), f.init)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSurvivingNativeApprovalCannotAcknowledgeRemovalAtSameHead(t *testing.T) {
	f := journalTestFixture(t)
	raw, err := os.ReadFile("../queue/testdata/wire/v1/event_review_stance_observed.json")
	if err != nil {
		t.Fatal(err)
	}
	native, err := queue.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	review := native.(queue.ReviewStanceObserved)
	review.Basis.ChangeID = queue.ChangeID(f.ref(3, 0).ChangeID)
	q := queue.Apply(queue.State{}, queue.ChangeRevisionObserved{PR: 2, ChangeID: review.Basis.ChangeID, Revision: 1, HeadSHA: review.Basis.Head, Basis: queue.RevisionObservationBasis{BaseSHA: "base", At: review.Basis.At}})
	q = queue.Apply(q, review)
	facts, ok := q.ApprovalFacts(review.Basis.ChangeID)
	if !ok || len(facts.Sources) != 1 {
		t.Fatal("missing surviving native approval")
	}
	nativeBytes, err := queue.EncodeSnapshot(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, optIn := range []bool{false, true} {
		s := f.start(t)
		s = applyJournal(t, s, f.policy(optIn, "author", "independent"))
		s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 0)}))
		s = applyJournal(t, s, f.declaration(t, s))
		assertHeld(t, s, f.alias, true)
		_, err := Apply(s, s.lastID+1, ackFor(t, s, f.alias, "author"))
		if !errors.Is(err, ErrHeld) {
			t.Fatal("writer-author used independent ack", err)
		}
		self := selfAck(ackFor(t, s, f.alias, "author"))
		next, err := Apply(s, s.lastID+1, self)
		if optIn {
			if err != nil {
				t.Fatal(err)
			}
			s = next
			assertHeld(t, s, f.alias, false)
		} else if !errors.Is(err, ErrHeld) {
			t.Fatal("native approval bypassed hold", err)
		}
		wrapped, err := EncodeSnapshot(q, s)
		if err != nil {
			t.Fatal(err)
		}
		restoredQ, restoredS, err := DecodeSnapshot(wrapped, f.init)
		if err != nil {
			t.Fatal(err)
		}
		after, err := queue.EncodeSnapshot(restoredQ)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(nativeBytes, after) || restoredS.Hold(f.alias).Held == optIn {
			t.Fatal("changed ordinary approvals or declaration authorization")
		}
		revision, _ := restoredQ.PRRevision(2)
		if revision.Revision != 1 || revision.HeadSHA != review.Basis.Head {
			t.Fatal("removal changed code revision")
		}
	}
}
