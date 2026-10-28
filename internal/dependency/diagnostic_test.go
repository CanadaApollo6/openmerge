package dependency

import (
	"bytes"
	"encoding/json"
 "errors"
 "strconv"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"reflect"
	"strings"
	"testing"
)

func diagnosticFixture(t *testing.T) (State, DiagnosticRecorded) {
	t.Helper()
	f := journalTestFixture(t)
	s := f.start(t)
	d := f.declaration(t, s, Input{Reference: f.ref(1, 7)})
	s = applyJournal(t, s, d)
	o := Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: 1, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("a", 40), Result: "waiting"}
	s = applyJournal(t, s, o)
	source := SourceAlias{f.alias.ForgeInstance, f.alias.Installation, "repo-api", 42}
	grant := Grant{"reader", "reader", source.RepositoryID, "source-read", true, true}
	return s, DiagnosticRecorded{Namespace: f.init.Namespace, ObservationEventID: s.lastID, Source: source, Evidence: ReadEvidence{Stage: WorkRead, Declaration: o.Declaration, Grant: grant, Observation: o, Code: "dependency-waiting"}}
}

func TestDiagnosticStrictCodec(t *testing.T) {
	_, e := diagnosticFixture(t)
	kind, raw, err := EncodeEvent(e)
	if err != nil || kind != "dependency_diagnostic_recorded" {
		t.Fatal(kind, err)
	}
	decoded, err := DecodeEvent(raw)
	if err != nil || !reflect.DeepEqual(e, decoded) {
		t.Fatal(decoded, err)
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
	if _, err := DecodeEvent(bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)); err == nil {
		t.Fatal("accepted duplicate field")
	}
}

func TestDiagnosticTypedCodesAndGuards(t *testing.T) {
	s, e := diagnosticFixture(t)
	for code, result := range map[string]string{"dependency-waiting": "waiting", "dependency-stale": "stale", "dependency-landed": "landed", "dependency-unavailable": "unavailable", "dependency-out-of-scope": "unavailable", "dependency-unresolved": "unavailable", "dependency-source-retargeted": "unavailable", "dependency-not-on-base": "unavailable", "dependency-unverified": "unavailable", "dependency-closed": "unavailable"} {
		t.Run(code, func(t *testing.T) {
			x := e
			x.Evidence.Code, x.Evidence.Observation.Result = code, result
			if x.Validate() != nil {
				t.Fatal("valid code rejected")
			}
			x.Evidence.Observation.Result = "landed"
			if result == "landed" {
				x.Evidence.Observation.Result = "waiting"
			}
			if x.Validate() == nil {
				t.Fatal("code/result mismatch accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*DiagnosticRecorded){
		"namespace":       func(x *DiagnosticRecorded) { x.Namespace = "bad" },
		"observation id":  func(x *DiagnosticRecorded) { x.ObservationEventID = 0 },
		"source":          func(x *DiagnosticRecorded) { x.Source.Number = 0 },
		"forge":           func(x *DiagnosticRecorded) { x.Source.ForgeInstance = "github:https://other.example" },
		"installation":    func(x *DiagnosticRecorded) { x.Source.Installation = "other" },
		"stage":           func(x *DiagnosticRecorded) { x.Evidence.Stage = "other" },
		"declaration":     func(x *DiagnosticRecorded) { x.Evidence.Declaration.EventID++ },
		"code":            func(x *DiagnosticRecorded) { x.Evidence.Code = "dependency-secret" },
		"grant repo":      func(x *DiagnosticRecorded) { x.Evidence.Grant.RepositoryID = "other" },
		"grant identity":  func(x *DiagnosticRecorded) { x.Evidence.Grant.ReturnedID = "other" },
		"grant freshness": func(x *DiagnosticRecorded) { x.Evidence.Grant.Fresh = false },
		"grant allowed":   func(x *DiagnosticRecorded) { x.Evidence.Grant.Allowed = false },
		"grant subject":   func(x *DiagnosticRecorded) { x.Evidence.Grant.SubjectID = "" },
		"grant returned":  func(x *DiagnosticRecorded) { x.Evidence.Grant.ReturnedID = "" },
		"grant read":      func(x *DiagnosticRecorded) { x.Evidence.Grant.ReadID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			x := e
			mutate(&x)
			if _, _, err := EncodeEvent(x); err == nil {
				t.Fatal("invalid diagnostic encoded")
			}
			if x.Scoped().View([]Grant{e.Evidence.Grant}).Code != "" {
				t.Fatal("invalid diagnostic disclosed")
			}
		})
	}
	for _, mutate := range []func(*DiagnosticRecorded){
		func(x *DiagnosticRecorded) { x.ObservationEventID++ },
		func(x *DiagnosticRecorded) { x.Evidence.Observation.SourceBase = strings.Repeat("b", 40) },
	} {
		x := e
		mutate(&x)
		if _, err := Apply(s, s.lastID+1, x); err == nil {
			t.Fatal("nonmatching observation accepted")
		}
	}
	x := e
	x.Evidence.Code, x.Evidence.Observation.Result = "dependency-unavailable", "unavailable"
	x.Evidence.Grant.Allowed, x.Evidence.Grant.Fresh = false, false
	if x.Validate() != nil {
		t.Fatal("failed permission diagnostic rejected")
	}
}

func TestDiagnosticProjectionRejectsInvalidContext(t *testing.T) {
	s, e := diagnosticFixture(t)
	for name, state := range map[string]State{"uninitialized": {}, "foreign": func() State {
		v := s
		i := *v.initialization
		i.Namespace = "omj1_" + strings.Repeat("b", 64)
		v.initialization = &i
		return v
	}()} {
		t.Run(name, func(t *testing.T) {
			if _, err := (DiagnosticProjection{}).Apply(state, s.lastID+1, e); err == nil {
				t.Fatal("invalid projection context accepted")
			}
		})
	}
	bad := e
	bad.Evidence.Code = "other"
	if _, err := (DiagnosticProjection{}).Apply(s, s.lastID+1, bad); err == nil {
		t.Fatal("invalid diagnostic projection accepted")
	}
}

func TestDiagnosticFullJournalProjectionAndRedaction(t *testing.T) {
	s, e := diagnosticFixture(t)
	prior := s
	s = applyJournal(t, s, e)
	p, err := (DiagnosticProjection{}).Apply(s, s.lastID, e)
	if err != nil {
		t.Fatal(err)
	}
	o, ok := p.Observation(s, e.Evidence.Observation)
	if !ok || o.View([]Grant{e.Evidence.Grant}).Code != e.Evidence.Code || o.View(nil).Code != "" {
		t.Fatal("diagnostic authorization", ok)
	}
	if _, ok := (DiagnosticProjection{}).Observation(s, e.Evidence.Observation); ok {
		t.Fatal("missing diagnostic invented")
	}
	if _, _, found := prior.Observation(e.Evidence.Observation); !found {
		t.Fatal("diagnostic mutated input observation")
	}
	next := e.Evidence.Observation
	next.ExpectedPrior, next.ReadSequence = e.ObservationEventID, next.ReadSequence+1
	s = applyJournal(t, s, next)
	if _, found := p.Observation(s, next); found {
		t.Fatal("reused stale diagnostic")
	}
	if _, err := p.Apply(s, s.lastID+1, e); err == nil {
		t.Fatal("stale diagnostic projected")
	}
}

func TestDiagnosticPreservesAdmissionAndSnapshotTail(t *testing.T) {
	s, e := diagnosticFixture(t)
	before := s.Admission(e.Evidence.Observation.ChangeID, e.Evidence.Observation.Revision)
	next := applyJournal(t, s, e)
	if !reflect.DeepEqual(before, next.Admission(e.Evidence.Observation.ChangeID, e.Evidence.Observation.Revision)) {
		t.Fatal("presentation companion changed admission")
	}
	raw, err := EncodeSnapshot(queue.State{}, next)
	if err != nil {
		t.Fatal(err)
	}
	_, restored, err := DecodeSnapshot(raw, *s.initialization)
	if err != nil {
		t.Fatal(err)
	}
	observation := e.Evidence.Observation
	observation.ExpectedPrior = e.ObservationEventID
	observation.ReadSequence++
	full, err := Apply(next, next.lastID+1, observation)
	if err != nil {
		t.Fatal(err)
	}
	tail, err := Apply(restored, next.lastID+1, observation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full, tail) {
		t.Fatal("diagnostic changed snapshot tail semantics")
	}
}

func TestDiagnosticProjectionLimit(t *testing.T) {
 s,e:=diagnosticFixture(t)
 p:=DiagnosticProjection{records:make(map[string]DiagnosticRecorded,maxProjectionEntries*MaxDirectEdges+1)}
 for n:=0;n<=maxProjectionEntries*MaxDirectEdges;n++{p.records[strconv.Itoa(n)]=e}
 if _,err:=p.Apply(s,s.lastID+1,e);!errors.Is(err,ErrLimit){t.Fatal("presentation history exceeded its bound",err)}
}

func TestDependencyEmptyDeclarationHasNoPresentation(t *testing.T) {
 f:=journalTestFixture(t)
 s:=f.start(t)
 d:=f.declaration(t,s)
 s=applyJournal(t,s,d)
 if _,shown:=s.Status(d.ChangeID,d.Revision,DiagnosticProjection{},nil);shown{t.Fatal("empty independent declaration gained dependency presentation")}
}
