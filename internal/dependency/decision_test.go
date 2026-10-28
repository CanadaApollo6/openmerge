package dependency

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// decisionFixture records policy, a revision-less declaration and a landed
// observation, then derives the member companion from Admission alone.
func decisionFixture(t *testing.T) (journalFixture, State, Observed, DecisionRecorded) {
	t.Helper()
	f := journalTestFixture(t)
	s := applyJournal(t, f.start(t), f.policy(false))
	d := f.declaration(t, s, Input{Reference: f.ref(1, 0)})
	s = applyJournal(t, s, d)
	o := Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: 1, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: Baseline{8, strings.Repeat("c", 64), strings.Repeat("c", 40)}, SourceBase: strings.Repeat("e", 40), Result: "landed"}
	s = applyJournal(t, s, o)
	a := s.Admission(d.ChangeID, 1)
	if a.Code != "" {
		t.Fatal("fixture not admitted", a.Code)
	}
	source := SourceSettlement{Reference: d.Edges[0].Reference, Queue: d.Edges[0].Queue, Source: SourceAlias{f.init.ForgeInstance, f.init.Installation, "repo-api-1", 42}, Grant: Grant{"author", "author", "repo-api-1", "landing-start-read", true, true}, Revision: o.SourceRevision, Commit: strings.Repeat("d", 40), SettlementEventID: 5, Watermark: 9, BaseSHA: o.SourceBase}
	c := DecisionRecorded{Namespace: f.init.Namespace, BatchID: "b1", AttemptID: "attempt", TestedSHA: strings.Repeat("1", 40), LandedSHA: strings.Repeat("1", 40), StartEventID: 20, DecisionID: 19, Member: 0, Members: 1, Alias: f.alias, ChangeID: d.ChangeID, Revision: 1, Head: strings.Repeat("2", 40), Admission: a.Basis, Sources: []SourceSettlement{source}}
	return f, s, o, c
}

func TestAdmissionFromRecordedFactsOnly(t *testing.T) {
	f, admitted, landed, c := decisionFixture(t)
	if a := admitted.Admission(c.ChangeID, 1); !reflect.DeepEqual(a.Basis.ObservationIDs, []uint64{admitted.lastID}) || len(a.Events) != 3 {
		t.Fatalf("admission basis/events %+v", a)
	}
	observe := func(s State, result string) State {
		o := landed
		o.ExpectedPrior, o.ReadSequence, o.Result = s.lastID, s.lastID+1, result
		return applyJournal(t, s, o)
	}
	base := applyJournal(t, applyJournal(t, f.start(t), f.policy(false)), f.declaration(t, applyJournal(t, f.start(t), f.policy(false)), Input{Reference: f.ref(1, 0)}))
	disabled := f.policy(false)
	disabled.Enabled, disabled.PriorEventID = false, 2
	disabled.Digest = PolicyDigest(disabled.Policy, false)
	excluded := f.policy(false)
	excluded.Policy.AllowFrom, excluded.PriorEventID = []string{}, 2
	excluded.Digest = PolicyDigest(excluded.Policy, true)
	for name, tc := range map[string]struct {
		s        State
		revision uint64
		want     string
	}{
		"undeclared":         {f.start(t), 1, "dependency-unresolved"},
		"other revision":     {admitted, 2, "dependency-unresolved"},
		"unobserved":         {base, 1, "dependency-waiting"},
		"waiting":            {observe(admitted, "waiting"), 1, "dependency-waiting"},
		"stale pin":          {observe(admitted, "stale"), 1, "dependency-stale"},
		"unavailable":        {observe(admitted, "unavailable"), 1, "dependency-unavailable"},
		"disabled policy":    {applyJournal(t, admitted, disabled), 1, "dependency-out-of-scope"},
		"no allow_from":      {applyJournal(t, admitted, excluded), 1, "dependency-out-of-scope"},
		"final edge removed": {applyJournal(t, admitted, f.declaration(t, admitted)), 1, "dependency-declaration-changed"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.s.Admission(c.ChangeID, tc.revision).Code; got != tc.want {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDecisionCompanionWireAndFold(t *testing.T) {
	f, s, landed, c := decisionFixture(t)
	kind, raw, err := EncodeEvent(c)
	if err != nil || kind != "dependency_decision_recorded" {
		t.Fatal(kind, err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(decoded, c) {
		t.Fatal("round trip", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	for _, path := range jsonFields(tree, nil) {
		for _, mode := range []string{"missing", "unknown", "null", "case"} {
			if _, err := DecodeEvent(mutateField(t, raw, path, mode)); err == nil {
				t.Fatalf("accepted %s at %v", mode, path)
			}
		}
	}
	next := applyJournal(t, s, c)
	if !reflect.DeepEqual(next.declarations, s.declarations) || !reflect.DeepEqual(next.observations, s.observations) {
		t.Fatal("historical companion mutated current side maps")
	}
	reject := func(name string, state State, e DecisionRecorded, want error) {
		t.Helper()
		if _, err := Apply(state, state.lastID+1, e); !errors.Is(err, want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	stale := c
	stale.Admission.ObservationIDs = []uint64{s.lastID - 1}
	reject("older observation basis", s, stale, ErrStale)
	grant := c
	grant.Sources = []SourceSettlement{c.Sources[0]}
	grant.Sources[0].Grant = Grant{"other", "other", "repo-api-1", "landing-start-read", true, true}
	reject("non-declarer source grant", s, grant, ErrStale)
	moved := c
	moved.Sources = []SourceSettlement{c.Sources[0]}
	moved.Sources[0].BaseSHA = strings.Repeat("f", 40)
	reject("base differs from observation", s, moved, ErrStale)
	waiting := landed
	waiting.ExpectedPrior, waiting.ReadSequence, waiting.Result = s.lastID, 2, "waiting"
	reject("newer waiting observation", applyJournal(t, s, waiting), c, ErrNotAdmitted)
	removed := f.declaration(t, s)
	reject("loosened declaration", applyJournal(t, s, removed), c, ErrNotAdmitted)
	for name, mutate := range map[string]func(*DecisionRecorded){
		"decision after start": func(e *DecisionRecorded) { e.DecisionID = e.StartEventID },
		"member out of range":  func(e *DecisionRecorded) { e.Member = e.Members },
		"missing source":       func(e *DecisionRecorded) { e.Sources = []SourceSettlement{} },
		"settlement watermark": func(e *DecisionRecorded) {
			e.Sources = []SourceSettlement{c.Sources[0]}
			e.Sources[0].Watermark = e.Sources[0].SettlementEventID - 1
		},
	} {
		e := c
		mutate(&e)
		if _, _, err := EncodeEvent(e); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}
