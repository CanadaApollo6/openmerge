package journal

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
	p "github.com/CanadaApollo6/openmerge/internal/provenance"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func oid(c string) string { return strings.Repeat(c, 40) }
func fixture(t *testing.T) (Observation, []p.Commit) {
	t.Helper()
	commits := []p.Commit{
		{ObjectID: oid("a"), Complete: true, Declarations: []p.Declaration{{Source: "entire", Version: "1", Kind: "entire-checkpoint", Value: "abcdef012345", Outcome: p.NotRequested, Class: p.Agent}}},
		{ObjectID: oid("b"), Complete: true, Declarations: []p.Declaration{}},
	}
	dd, err := p.DeclarationDigest(commits, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := p.NewParserPolicy("git version 2.56.0")
	h := Observation{
		Request: p.InventoryRequest{Repository: "https://github.com/example/repo", Queue: "main", ChangeID: "change-1", Revision: "1", Generation: "generation-1", Head: oid("b"), PinnedBase: oid("c"), BuildBase: oid("d"), Excluded: []string{}},
		Parser:  policy, ParserID: policy.Identity(), Policy: PolicySource{DefaultBranch: "main", ReadRef: "refs/heads/main", Commit: oid("e"), EffectiveDigest: strings.Repeat("f", 64)},
		Limits: p.DefaultInventoryLimits(), Stable: []string{oid("a")}, Additional: []string{oid("b")}, MergeBases: []string{oid("c")}, BuildMergeBases: []string{oid("d")}, Complete: true, Diagnostic: p.Diagnostic{Outcome: p.ReadComplete}, DeclarationDigest: dd, Chunks: 1, Commits: commits,
	}
	h.Digest = InventoryDigest(h, commits)
	return h, commits
}
func chunks(h Observation, commits []p.Commit) []Observation {
	a, b := h, h
	a.Chunks, b.Chunks = 2, 2
	a.Commits = commits[:1]
	b.Commits = commits[1:]
	b.Index = 1
	return []Observation{a, b}
}
func landing(h Observation, ids []uint64) Landing {
	pairs := []p.CommitMapping{{Original: oid("a"), Build: oid("a")}, {Original: oid("b"), Build: oid("b")}}
	m := Member{Request: h.Request, ObservationIDs: ids, InventoryDigest: h.Digest, DeclarationDigest: h.DeclarationDigest,
		Mapping: p.BuildMapping{Request: h.Request, PolicyID: h.ParserID, Build: p.BuildObservation{ID: "composition-T", Stage: "tested", Method: "merge", Base: h.Request.BuildBase, Result: h.Request.Head, Replays: []p.CommitMapping{}}, Commits: pairs, Complete: true, Diagnostic: p.Diagnostic{Outcome: p.ReadComplete}},
		Layers: []Layer{
			{Kind: "baseline", Expression: "check-success = ci", Inputs: []Input{{Name: "check-success", Type: "list", Known: true, List: []string{"ci"}}}, Eligibility: []string{"baseline-reviewer"}, References: []uint64{1}, Truth: "true"},
			{Kind: "trigger", Rule: "agent-review", Expression: "#agent-commits > 0", Inputs: []Input{{Name: "agent-commits", Type: "list", Known: true, List: []string{oid("a")}}}, Eligibility: []string{"baseline-reviewer"}, References: []uint64{}, Truth: "true"},
			{Kind: "requirement", Rule: "agent-review", Expression: "human approval", Inputs: []Input{{Name: "approved-reviews-by", Type: "list", Known: true, List: []string{"human-reviewer"}}}, Eligibility: []string{"baseline-reviewer", "human-reviewer"}, References: []uint64{2}, Truth: "true"},
		}, Result: "true"}
	m.RecordDigest = MemberDigest(m)
	return Landing{AttemptID: "attempt-1", BatchID: "batch-1", DecisionID: 10, Stage: "tested", Members: []Member{m}}
}
func apply(t *testing.T, s State, id uint64, e Event) State {
	t.Helper()
	next, err := Apply(s, id, e)
	if err != nil {
		t.Fatalf("apply %d %T: %v", id, e, err)
	}
	return next
}
func snapshotBytes(t *testing.T, base []byte, s State) []byte {
	t.Helper()
	if len(s.records) == 0 && !s.compacted() {
		return base
	}
	var header struct{ Version int }
	if err := json.Unmarshal(base, &header); err != nil {
		t.Fatal(err)
	}
	kind := "queue"
	if header.Version == 8 {
		kind = "dependency"
	}
	raw, err := EncodeSnapshot(kind, base, s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func goldenEvents(t *testing.T) map[string]Event {
	h, c := fixture(t)
	cc := chunks(h, c)
	terminal := h
	terminal.Request.Generation = "generation-failed"
	terminal.Complete = false
	terminal.Diagnostic = p.Diagnostic{Outcome: p.LimitExceeded, Resource: "graph"}
	terminal.DeclarationDigest = ""
	terminal.Digest = InventoryDigest(terminal, c)
	pending := terminal
	pending.Request.Generation = "generation-pending"
	pending.Diagnostic = p.Diagnostic{Outcome: p.ReadPending, Resource: "git"}
	pending.Commits = []p.Commit{}
	pending.Stable = []string{}
	pending.Additional = []string{}
	pending.Digest = InventoryDigest(pending, pending.Commits)
	tested := landing(h, []uint64{3, 5})
	landed := tested
	landed.Stage = "landed"
	landed.Members = append([]Member{}, tested.Members...)
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].Mapping.Build.ID = "composition-L"
	landed.Members[0].Mapping.Build.Result = oid("f")
	landed.Members[0].RecordDigest = MemberDigest(landed.Members[0])
	return map[string]Event{"complete": h, "chunk-0": cc[0], "chunk-1": cc[1], "terminal": terminal, "pending": pending, "tested": tested, "landed": landed}
}
func TestGoldenPayloadsAndStrictOldDecoderRefusal(t *testing.T) {
	for name, e := range goldenEvents(t) {
		t.Run(name, func(t *testing.T) {
			_, raw, err := EncodeEvent(e)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, raw) {
				t.Fatalf("golden changed: %s", raw)
			}
			decoded, err := DecodeEvent(want)
			if err != nil || !reflect.DeepEqual(decoded, e) {
				t.Fatalf("roundtrip: %v", err)
			}
			_, again, err := EncodeEvent(decoded)
			if err != nil || !bytes.Equal(again, want) {
				t.Fatal("re-encoding changed", err)
			}
			if _, err := queue.DecodeEvent(raw); err == nil {
				t.Fatal("old queue decoder accepted provenance")
			}
			if _, err := dependency.DecodeEvent(raw); err == nil {
				t.Fatal("v8 dependency decoder accepted provenance")
			}
		})
	}
}
func TestCompleteChunkedInventoryAndGenerationBindings(t *testing.T) {
	h, c := fixture(t)
	cc := chunks(h, c)
	full := apply(t, State{}, 3, h)
	partial := apply(t, State{}, 3, cc[0])
	if _, ok := partial.Inventory(h.Request); ok {
		t.Fatal("partial inventory complete")
	}
	l := landing(h, []uint64{3})
	if _, err := Apply(partial, 11, l); !errors.Is(err, ErrIncomplete) {
		t.Fatal("partial authorized", err)
	}
	chunked := apply(t, partial, 5, cc[1])
	fi, ok := full.Inventory(h.Request)
	if !ok {
		t.Fatal("complete missing")
	}
	ci, ok := chunked.Inventory(h.Request)
	if !ok || !reflect.DeepEqual(fi.Commits, ci.Commits) {
		t.Fatal("inventory mismatch")
	}
	if InventoryDigest(fi.Header, fi.Commits) != InventoryDigest(ci.Header, ci.Commits) {
		t.Fatal("chunk framing entered digest")
	}
	apply(t, chunked, 11, landing(h, []uint64{3, 5}))
	for name, mutate := range map[string]func(*Landing){
		"wrong refs": func(l *Landing) { l.Members[0].ObservationIDs = []uint64{3, 4} },
		"revision": func(l *Landing) {
			l.Members[0].Request.Revision = "2"
			l.Members[0].Mapping.Request = l.Members[0].Request
		},
		"generation": func(l *Landing) {
			l.Members[0].Request.Generation = "other"
			l.Members[0].Mapping.Request = l.Members[0].Request
		},
		"head": func(l *Landing) {
			l.Members[0].Request.Head = oid("f")
			l.Members[0].Mapping.Request = l.Members[0].Request
		},
		"base rewrite": func(l *Landing) {
			l.Members[0].Request.PinnedBase = oid("f")
			l.Members[0].Mapping.Request = l.Members[0].Request
		},
		"policy":           func(l *Landing) { l.Members[0].Mapping.PolicyID = strings.Repeat("0", 64) },
		"digest":           func(l *Landing) { l.Members[0].InventoryDigest = strings.Repeat("0", 64) },
		"missing original": func(l *Landing) { l.Members[0].Mapping.Commits = l.Members[0].Mapping.Commits[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			l := landing(h, []uint64{3, 5})
			mutate(&l)
			l.Members[0].RecordDigest = MemberDigest(l.Members[0])
			before := snapshotBytes(t, []byte(`{"Version":6}`), chunked)
			next, err := Apply(chunked, 11, l)
			if err == nil || !bytes.Equal(before, snapshotBytes(t, []byte(`{"Version":6}`), next)) {
				t.Fatal("bad binding accepted or changed state", err)
			}
		})
	}
	newer := h
	newer.Request.Generation = "generation-2"
	newer.PreviousObservationID = 3
	newer.Digest = InventoryDigest(newer, c)
	refreshed := apply(t, chunked, 7, newer)
	if _, err := Apply(refreshed, 11, landing(h, []uint64{3, 5})); err == nil {
		t.Fatal("stale generation authorized")
	}
	newer.Request.Generation = "generation-3"
	newer.PreviousObservationID = 3
	newer.Digest = InventoryDigest(newer, c)
	if _, err := Apply(refreshed, 12, newer); err == nil {
		t.Fatal("stale CAS accepted")
	}
	// Git upgrades change both parser identity and canonical record identity.
	newer = h
	newer.Parser = p.NewParserPolicy("git version 2.55.0")
	newer.ParserID = newer.Parser.Identity()
	if InventoryDigest(newer, c) == h.Digest || newer.ParserID == h.ParserID {
		t.Fatal("Git version not bound")
	}
}
func TestInventoryChunkFailuresAndOutcomes(t *testing.T) {
	h, c := fixture(t)
	cc := chunks(h, c)
	partial := apply(t, State{}, 3, cc[0])
	for name, mutate := range map[string]func(*Observation){
		"different header":   func(h *Observation) { h.Request.Revision = "2" },
		"duplicate index":    func(h *Observation) { h.Index = 0 },
		"digest":             func(h *Observation) { h.Digest = strings.Repeat("0", 64) },
		"duplicate original": func(h *Observation) { h.Commits = []p.Commit{c[0]} },
		"missing original":   func(h *Observation) { h.Commits = []p.Commit{} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cc[1]
			mutate(&bad)
			if _, err := Apply(partial, 5, bad); err == nil {
				t.Fatal("accepted invalid chunk")
			}
		})
	}
	if _, err := Apply(State{}, 3, cc[1]); err == nil {
		t.Fatal("accepted chunk before header")
	}
	for _, outcome := range []p.ReadOutcome{p.ReadPending, p.LimitExceeded, p.InvalidInput} {
		failed := h
		failed.Complete = false
		failed.Diagnostic = p.Diagnostic{Outcome: outcome, Resource: "graph"}
		failed.DeclarationDigest = ""
		failed.Digest = InventoryDigest(failed, c)
		s := apply(t, State{}, 3, failed)
		v, ok := s.Inventory(failed.Request)
		if ok || v.Header.Diagnostic.Outcome != outcome {
			t.Fatal("lost failure outcome")
		}
		if _, err := Apply(s, 11, landing(h, []uint64{3})); err == nil {
			t.Fatal("failure authorized landing")
		}
	}
	for name, mutate := range map[string]func(*Observation){
		"overlap partitions": func(h *Observation) { h.Additional = h.Stable },
		"excluded":           func(h *Observation) { h.Request.Excluded = []string{oid("a")} },
		"incomplete commit":  func(h *Observation) { h.Commits = append([]p.Commit{}, c...); h.Commits[0].Complete = false },
		"parser identity":    func(h *Observation) { h.Parser.GitVersion = "git version 2.55.0" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := h
			mutate(&bad)
			bad.Digest = InventoryDigest(bad, bad.Commits)
			if _, err := Apply(State{}, 3, bad); err == nil {
				t.Fatal("accepted bad inventory")
			}
		})
	}
	empty := h
	empty.Stable = []string{}
	empty.Additional = []string{}
	empty.Commits = []p.Commit{}
	empty.DeclarationDigest, _ = p.DeclarationDigest(empty.Commits, true, nil)
	empty.Digest = InventoryDigest(empty, empty.Commits)
	s := apply(t, State{}, 3, empty)
	if _, ok := s.Inventory(empty.Request); !ok {
		t.Fatal("empty complete inventory lost")
	}
}
func TestLandingLayersMembersAndTestedLandedBindings(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	tested := landing(h, []uint64{3})
	s = apply(t, s, 11, tested)
	landed := landing(h, []uint64{3})
	landed.Stage = "landed"
	landed.DecisionID = 12
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].RecordDigest = MemberDigest(landed.Members[0])
	if _, err := Apply(s, 13, landed); err == nil {
		t.Fatal("landed reused tested composition ID")
	}
	landed.Members[0].Mapping.Build.ID = "composition-L"
	landed.Members[0].Mapping.Build.Result = oid("f")
	landed.Members[0].RecordDigest = MemberDigest(landed.Members[0])
	apply(t, s, 13, landed)
	for _, trigger := range []string{"true", "false", "pending"} {
		for _, requirement := range []string{"true", "false", "pending"} {
			l := landing(h, []uint64{3})
			m := &l.Members[0]
			m.Layers[1].Truth = trigger
			m.Layers[2].Truth = requirement
			want := "pending"
			if trigger == "false" || requirement == "true" {
				want = "true"
			} else if trigger == "true" && requirement == "false" {
				want = "false"
			}
			m.Result = want
			m.RecordDigest = MemberDigest(*m)
			apply(t, apply(t, State{}, 3, h), 11, l)
			if want != "true" {
				m.Result = "true"
				m.RecordDigest = MemberDigest(*m)
				if l.Validate() == nil {
					t.Fatal("unknown/false result passed")
				}
			}
		}
	}
	// Each batch member needs its own generation and mapping, even for shared originals.
	other := h
	other.Request.ChangeID = "change-2"
	other.Request.Generation = "other-generation"
	other.Digest = InventoryDigest(other, other.Commits)
	both := apply(t, apply(t, State{}, 3, h), 5, other)
	l := landing(h, []uint64{3})
	second := landing(other, []uint64{5})
	l.Members = append(l.Members, second.Members[0])
	apply(t, both, 11, l)
	l.Members[1].ObservationIDs = []uint64{3}
	l.Members[1].RecordDigest = MemberDigest(l.Members[1])
	if _, err := Apply(both, 11, l); err == nil {
		t.Fatal("pooled member evidence")
	}
}
func TestStateDoesNotAliasCallerEvidence(t *testing.T) {
	h, _ := fixture(t)
	s := apply(t, State{}, 3, h)
	h.Commits[0].Declarations[0].Value = "changed"
	inv, ok := s.Inventory(h.Request)
	if !ok || inv.Commits[0].Declarations[0].Value == "changed" {
		t.Fatal("input alias")
	}
	inv.Commits[0].ObjectID = "changed"
	records := s.Records()
	records[0].Payload[0] = '!'
	inv, _ = s.Inventory(h.Request)
	if inv.Commits[0].ObjectID == "changed" {
		t.Fatal("accessor alias")
	}
	if _, err := DecodeEvent(s.Records()[0].Payload); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPlusTailEveryCutWithDependencyV8AndHeldRequests(t *testing.T) {
	h, c := fixture(t)
	cc := chunks(h, c)
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "installation-1")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := dependency.Apply(dependency.State{}, 1, init)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	held := queue.Apply(queue.State{}, queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"})
	held = queue.Apply(held, queue.EnqueueRequested{PR: 2, At: at})
	base, err := dependency.EncodeSnapshot(held, ds)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(_ string, raw []byte) error {
		q, d, err := dependency.DecodeSnapshot(raw, init)
		if err != nil {
			return err
		}
		again, err := dependency.EncodeSnapshot(q, d)
		if err != nil {
			return err
		}
		if !bytes.Equal(again, base) || len(q.HeldRequests()) != 1 {
			return fmt.Errorf("lost dependency or #163 held requests")
		}
		return nil
	}
	l := landing(h, []uint64{3, 5})
	landed := landing(h, []uint64{3, 5})
	landed.Stage = "landed"
	landed.Members[0].Mapping.Build.Stage = "landed"
	landed.Members[0].Mapping.Build.ID = "composition-L"
	landed.Members[0].RecordDigest = MemberDigest(landed.Members[0])
	newer := h
	newer.Request.Generation = "generation-2"
	newer.PreviousObservationID = 3
	newer.Digest = InventoryDigest(newer, c)
	steps := []struct {
		id uint64
		e  Event
	}{{3, cc[0]}, {5, cc[1]}, {11, l}, {13, landed}, {15, newer}}
	for cut := 0; cut <= len(steps); cut++ {
		var full, tail State
		for i := 0; i <= len(steps); i++ {
			if i == cut {
				raw := snapshotBytes(t, base, tail)
				decoded, err := DecodeSnapshot(raw, decode)
				if err != nil {
					t.Fatalf("cut %d: %v", cut, err)
				}
				tail = decoded.Provenance
				if !bytes.Equal(base, decoded.Base) {
					t.Fatal("base bytes changed")
				}
			}
			if !bytes.Equal(snapshotBytes(t, base, full), snapshotBytes(t, base, tail)) {
				t.Fatalf("cut %d step %d diverged", cut, i)
			}
			if i < len(steps) {
				full = apply(t, full, steps[i].id, steps[i].e)
				tail = apply(t, tail, steps[i].id, steps[i].e)
			}
		}
	}
	raw := snapshotBytes(t, base, apply(t, State{}, 3, h))
	if _, err := queue.DecodeSnapshot(raw); err == nil {
		t.Fatal("old snapshot reader accepted v9")
	}
	if _, _, err := dependency.DecodeSnapshot(raw, init); err == nil {
		t.Fatal("v8 reader accepted v9")
	}
	if _, err := DecodeSnapshot(raw, func(_ string, raw []byte) error {
		_, _, err := dependency.DecodeSnapshot(raw, dependency.JournalInitialized{})
		return err
	}); err == nil {
		t.Fatal("dependency identity bypass")
	}
}
func TestLegacySnapshotBytesRemainUnchanged(t *testing.T) {
	raw, err := os.ReadFile("../../dependency/testdata/legacy-held.snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var q queue.State
	decoded, err := DecodeSnapshot(raw, func(_ string, b []byte) error { var err error; q, err = queue.DecodeSnapshot(b); return err })
	if err != nil {
		t.Fatal(err)
	}
	again, err := queue.EncodeSnapshot(q)
	if err != nil || !bytes.Equal(raw, again) || !bytes.Equal(raw, decoded.Base) || len(decoded.Provenance.Records()) != 0 {
		t.Fatal("legacy bytes changed", err)
	}
}

func TestInventoryAtSourceCeilingAndBoundedChunks(t *testing.T) {
	h, _ := fixture(t)
	commits := make([]p.Commit, h.Limits.SourceCommits)
	ids := make([]string, len(commits))
	for i := range commits {
		id := fmt.Sprintf("%040x", i+1)
		ids[i] = id
		commits[i] = p.Commit{ObjectID: id, Complete: true, Declarations: []p.Declaration{}}
	}
	h.Stable = ids
	h.Additional = []string{}
	h.Commits = []p.Commit{}
	h.Chunks = len(commits) / maxChunkCommits
	h.Request.Head = ids[len(ids)-1]
	var err error
	h.DeclarationDigest, err = p.DeclarationDigest(commits, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Digest = InventoryDigest(h, commits)
	var s State
	for i := 0; i < h.Chunks; i++ {
		chunk := h
		chunk.Index = i
		chunk.Commits = commits[i*maxChunkCommits : (i+1)*maxChunkCommits]
		s = apply(t, s, uint64(i+1), chunk)
	}
	inv, ok := s.Inventory(h.Request)
	if !ok || len(inv.Commits) != h.Limits.SourceCommits {
		t.Fatal("ceiling inventory incomplete")
	}
	oversized := h
	oversized.Index = 0
	oversized.Commits = commits[:maxChunkCommits+1]
	if _, _, err := EncodeEvent(oversized); !errors.Is(err, ErrLimit) {
		t.Fatal("oversized chunk accepted", err)
	}
	h.Limits.SourceCommits--
	if h.Validate() == nil {
		t.Fatal("raised effective source ceiling")
	}
}

func TestPendingCommitEvidenceAndRebuildFences(t *testing.T) {
	h, c := fixture(t)
	pending := h
	pending.Complete = false
	pending.DeclarationDigest = ""
	pending.Diagnostic = p.Diagnostic{Outcome: p.ReadPending, Resource: "commit", ObjectID: oid("a")}
	pending.Commits = append([]p.Commit{}, c...)
	pending.Commits[0].Complete = false
	pending.Commits[0].Declarations = []p.Declaration{}
	pending.Digest = InventoryDigest(pending, pending.Commits)
	s := apply(t, State{}, 3, pending)
	inv, ok := s.Inventory(h.Request)
	if ok || inv.Commits[0].Complete {
		t.Fatal("incomplete extractor promoted")
	}
	h.Request.Generation = "generation-2"
	h.PreviousObservationID = 3
	h.Digest = InventoryDigest(h, c)
	s = apply(t, s, 5, h)
	l := landing(h, []uint64{5})
	s = apply(t, s, 11, l)
	// Rebuilds have a new composition observation in a distinct attempt, while
	// retaining the original revision and inventory generation.
	rebuilt := landing(h, []uint64{5})
	rebuilt.AttemptID = "attempt-rebuild"
	rebuilt.Members[0].Mapping.Build.ID = "composition-rebuild"
	rebuilt.Members[0].RecordDigest = MemberDigest(rebuilt.Members[0])
	s = apply(t, s, 13, rebuilt)
	pushed := h
	pushed.Request.Revision = "2"
	pushed.Request.Generation = "generation-repush"
	pushed.Request.Head = oid("f")
	pushed.PreviousObservationID = 5
	pushed.Digest = InventoryDigest(pushed, c)
	s = apply(t, s, 15, pushed)
	if _, err := Apply(s, 17, landing(h, []uint64{5})); err == nil {
		t.Fatal("re-push retained old eligibility")
	}
}

func TestTypedGateInputsAndDeclarationDigestSeparation(t *testing.T) {
	h, _ := fixture(t)
	l := landing(h, []uint64{3})
	m := &l.Members[0]
	before := m.RecordDigest
	m.Layers[2].Inputs = []Input{
		{Name: "checkpoint-present", Type: "bool", Known: false, List: []string{}},
		{Name: "provenance", Type: "string", Known: true, String: "agent", List: []string{}},
	}
	m.Layers[2].Truth = "pending"
	m.Result = "pending"
	m.RecordDigest = MemberDigest(*m)
	if m.RecordDigest == before || m.DeclarationDigest != h.DeclarationDigest {
		t.Fatal("mutable gate evidence changed receipt identity")
	}
	apply(t, apply(t, State{}, 3, h), 11, l)
	m.Layers[2].Inputs[0].Bool = true
	m.RecordDigest = MemberDigest(*m)
	if l.Validate() == nil {
		t.Fatal("unknown input retained a passing value")
	}
}

func TestSnapshotPlusMixedQueueTail(t *testing.T) {
	h, c := fixture(t)
	cc := chunks(h, c)
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "123")
	if err != nil {
		t.Fatal(err)
	}
	ds, err := dependency.Apply(dependency.State{}, 1, init)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		id uint64
		q  queue.Event
		p  Event
	}{
		{id: 2, q: queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}},
		{id: 3, q: queue.EnqueueRequested{PR: 2, At: at}},
		{id: 5, p: cc[0]},
		{id: 6, q: queue.BaseMoved{SHA: "base"}},
		{id: 7, p: cc[1]},
		{id: 8, q: queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, Eligible: true}},
		{id: 11, p: landing(h, []uint64{5, 7})},
		{id: 12, q: queue.DequeueRequested{PR: 2}},
	}
	encode := func(q queue.State, s State) []byte {
		t.Helper()
		base, err := dependency.EncodeSnapshot(q, ds)
		if err != nil {
			t.Fatal(err)
		}
		return snapshotBytes(t, base, s)
	}
	for cut := 0; cut <= len(steps); cut++ {
		var fullQ, tailQ queue.State
		var fullS, tailS State
		for i := 0; i <= len(steps); i++ {
			if i == cut {
				decoded, err := DecodeSnapshot(encode(tailQ, tailS), func(_ string, base []byte) error {
					var err error
					tailQ, _, err = dependency.DecodeSnapshot(base, init)
					return err
				})
				if err != nil {
					t.Fatalf("cut %d: %v", cut, err)
				}
				tailS = decoded.Provenance
			}
			cfg := queue.Config{MergeMethod: queue.Merge, SpeculationMinDepth: 1, SpeculationMaxDepth: 4}
			if !bytes.Equal(encode(fullQ, fullS), encode(tailQ, tailS)) || !reflect.DeepEqual(queue.Plan(cfg, fullQ, at), queue.Plan(cfg, tailQ, at)) {
				t.Fatalf("cut %d step %d diverged", cut, i)
			}
			if i < len(steps) {
				step := steps[i]
				if step.q != nil {
					fullQ = queue.Apply(fullQ, step.q)
					tailQ = queue.Apply(tailQ, step.q)
				} else {
					fullS = apply(t, fullS, step.id, step.p)
					tailS = apply(t, tailS, step.id, step.p)
				}
			}
		}
	}
}
