package dependency

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func codecEvents(t *testing.T) []Event {
	f := journalTestFixture(t)
	s := f.start(t)
	p := f.policy(true, "author", "independent")
	s = applyJournal(t, s, p)
	d := f.declaration(t, s, Input{Reference: f.ref(1, 7)})
	s = applyJournal(t, s, d)
	o := Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: 1, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("a", 40), Result: "landed"}
	remove := f.declaration(t, s)
	s = applyJournal(t, s, remove)
	return []Event{f.init, p, d, o, remove, ackFor(t, s, f.alias, "independent"), selfAck(ackFor(t, s, f.alias, "author"))}
}

type fieldPath []any

func jsonFields(value any, path fieldPath) []fieldPath {
	var paths []fieldPath
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			p := append(append(fieldPath{}, path...), key)
			paths = append(paths, p)
			paths = append(paths, jsonFields(child, p)...)
		}
	case []any:
		for i, child := range v {
			p := append(append(fieldPath{}, path...), i)
			paths = append(paths, jsonFields(child, p)...)
		}
	}
	return paths
}
func mutateField(t *testing.T, raw []byte, path fieldPath, mode string) []byte {
	t.Helper()
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	parent := tree
	for _, part := range path[:len(path)-1] {
		switch p := part.(type) {
		case string:
			parent = parent.(map[string]any)[p]
		case int:
			parent = parent.([]any)[p]
		}
	}
	fields := parent.(map[string]any)
	name := path[len(path)-1].(string)
	switch mode {
	case "missing":
		delete(fields, name)
	case "unknown":
		fields[name+"_future"] = fields[name]
	case "null":
		fields[name] = nil
	case "case":
		fields[strings.ToUpper(name)] = fields[name]
		delete(fields, name)
	}
	out, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDependencyWireStrictRequiredUnknownNullAndRoundTrip(t *testing.T) {
	for i, event := range codecEvents(t) {
		t.Run(fmt.Sprintf("%d-%T", i, event), func(t *testing.T) {
			kind, raw, err := EncodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEvent(raw)
			if err != nil || !reflect.DeepEqual(event, decoded) {
				t.Fatal("round trip", err, decoded)
			}
			againKind, again, err := EncodeEvent(decoded)
			if err != nil || kind != againKind || !bytes.Equal(raw, again) {
				t.Fatal("wire changed", err)
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
			duplicate := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
			for _, bad := range [][]byte{duplicate, append(append([]byte{}, raw...), []byte(`{}`)...), bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1), bytes.Replace(raw, []byte(kind), []byte("dependency_future"), 1)} {
				if _, err := DecodeEvent(bad); err == nil {
					t.Fatal("accepted corrupt envelope")
				}
			}
		})
	}
	f := journalTestFixture(t)
	_, raw, err := EncodeEvent(f.init)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := bytes.Replace(raw, []byte(`"installation":"123"`), []byte(`"installation":"123","installation":"123"`), 1)
	if _, err := DecodeEvent(duplicate); err == nil {
		t.Fatal("duplicate nested key accepted")
	}
	if _, err := DecodeEvent(bytes.Repeat([]byte(" "), maxEventBytes+1)); err == nil {
		t.Fatal("unbounded event")
	}
}

func TestDependencySnapshotStrictValidation(t *testing.T) {
	f := journalTestFixture(t)
	s := f.start(t)
	s = applyJournal(t, s, f.policy(true, "author", "independent"))
	s = applyJournal(t, s, f.declaration(t, s, Input{Reference: f.ref(1, 7)}))
	s = applyJournal(t, s, f.declaration(t, s))
	s = applyJournal(t, s, selfAck(ackFor(t, s, f.alias, "author")))
	raw := snapshotBytes(t, s)
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	for _, path := range jsonFields(tree, nil) {
		// The embedded queue codec deliberately retains its own compatibility rules.
		if path[0] == "Queue" {
			continue
		}
		for _, mode := range []string{"missing", "unknown", "case"} {
			if _, _, err := DecodeSnapshot(mutateField(t, raw, path, mode), f.init); err == nil {
				t.Fatalf("accepted snapshot %s at %v", mode, path)
			}
		}
	}
	var v snapshot
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*snapshot){
		func(v *snapshot) { v.Declarations = append(v.Declarations, v.Declarations[0]) },
		func(v *snapshot) {
			v.Declarations[0].Ack.Governing.Policy.SingleMaintainer = false
			v.Declarations[0].Ack.Governing.Digest = PolicyDigest(v.Declarations[0].Ack.Governing.Policy, v.Declarations[0].Ack.Governing.Enabled)
			v.Declarations[0].Ack.Event.PolicyDigest = v.Declarations[0].Ack.Governing.Digest
		},
		func(v *snapshot) { v.Declarations[0].Ack.Overridden = []string{} },
		func(v *snapshot) { v.Declarations[0].Unknown = true },
		func(v *snapshot) { v.Declarations[0].Ack.Revoked = true },
		func(v *snapshot) { v.Declarations[0].Ack.Kind = "dependency_declaration_acknowledged" },
		func(v *snapshot) { v.Declarations[0].EventID = v.LastID + 1 },
		func(v *snapshot) { v.Policies[0].Event.ReadRef = "refs/pull/2/head" },
	} {
		var bad snapshot
		if err := json.Unmarshal(raw, &bad); err != nil {
			t.Fatal(err)
		}
		mutate(&bad)
		b, err := json.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := DecodeSnapshot(b, f.init); err == nil {
			t.Fatal("accepted inconsistent snapshot")
		}
	}
	if _, _, err := DecodeSnapshot(bytes.Repeat([]byte(" "), maxSnapshotBytes+1), f.init); err == nil {
		t.Fatal("unbounded snapshot")
	}
}

type replayStep struct {
	q queue.Event
	d Event
}

func replayAt(t *testing.T, q queue.State, s State, i int, step replayStep) (queue.State, State) {
	t.Helper()
	if step.q != nil {
		_, raw, err := queue.EncodeEvent(step.q)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := queue.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		q = queue.Apply(q, decoded)
	} else {
		_, raw, err := EncodeEvent(step.d)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		s, err = Apply(s, uint64(i+1), decoded)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	return q, s
}
func TestReplayEverySnapshotTailWithHeldRequests(t *testing.T) {
	f := journalTestFixture(t)
	var steps []replayStep
	var q queue.State
	var s State
	add := func(step replayStep) { steps = append(steps, step); q, s = replayAt(t, q, s, len(steps)-1, step) }
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	add(replayStep{q: queue.BaseMoved{SHA: "base"}})
	add(replayStep{d: f.init})
	add(replayStep{d: f.policy(true, "author", "independent")})
	policyID := s.lastID
	add(replayStep{d: f.declaration(t, s, Input{Reference: f.ref(1, 0)})})
	d, _ := s.Declaration(f.alias)
	add(replayStep{d: Observed{Namespace: f.init.Namespace, Alias: f.alias, ChangeID: d.ChangeID, Revision: 1, Declaration: s.Prior(f.alias), Source: d.Edges[0].Reference, ReadSequence: 1, SourceRevision: f.baseline(1), SourceBase: strings.Repeat("e", 40), Result: "waiting"}})
	add(replayStep{q: queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, PendingConditions: "sign-off"}})
	add(replayStep{q: queue.EnqueueRequested{PR: 2, At: at}})
	add(replayStep{d: f.declaration(t, s)})
	// Snapshot here contains BOTH #163's exact-head held request and the alias hold.
	if len(q.HeldRequests()) != 1 || !s.Hold(f.alias).Held {
		t.Fatal("missing combined holds fixture")
	}
	add(replayStep{d: selfAck(ackFor(t, s, f.alias, "author"))})
	revoked := f.policy(false, "author", "independent")
	revoked.PriorEventID = policyID
	add(replayStep{d: revoked})
	add(replayStep{q: queue.PRObserved{PR: 2, HeadSHA: "head", Trusted: true, Eligible: true}})
	if len(q.Entries) != 1 || !s.Hold(f.alias).Held {
		t.Fatal("held-request admission lost declaration hold")
	}
	add(replayStep{d: f.declaration(t, s, Input{Reference: f.ref(1, 7)})})
	add(replayStep{q: queue.DequeueRequested{PR: 2}})
	add(replayStep{q: queue.EnqueueRequested{PR: 2, At: at}})
	assertHeld(t, s, f.alias, true)
	add(replayStep{d: ackFor(t, s, f.alias, "independent")})
	add(replayStep{q: queue.PRObserved{PR: 3, HeadSHA: "old", Trusted: true, PendingConditions: "label"}})
	add(replayStep{q: queue.EnqueueRequested{PR: 3, At: at}})
	add(replayStep{q: queue.PRObserved{PR: 3, HeadSHA: "new", Trusted: true, Eligible: true}})
	if len(q.HeldRequests()) != 0 || len(q.Entries) != 1 {
		t.Fatal("head change reused old request")
	}
	// Every cut, every subsequent revision, and both planner depths. No sleeps,
	// database reads or forge calls can affect this differential replay.
	for _, depth := range []int{1, 4} {
		cfg := queue.Config{MergeMethod: queue.Merge, SpeculationMinDepth: 1, SpeculationMaxDepth: depth}
		for cut := 0; cut <= len(steps); cut++ {
			var fullQ, tailQ queue.State
			var fullS, tailS State
			for i := 0; i <= len(steps); i++ {
				if i == cut {
					raw, err := EncodeSnapshot(tailQ, tailS)
					if err != nil {
						t.Fatal(err)
					}
					tailQ, tailS, err = DecodeSnapshot(raw, f.init)
					if err != nil {
						t.Fatalf("cut %d: %v", cut, err)
					}
				}
				want, err := EncodeSnapshot(fullQ, fullS)
				if err != nil {
					t.Fatal(err)
				}
				got, err := EncodeSnapshot(tailQ, tailS)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(want, got) || !reflect.DeepEqual(queue.Plan(cfg, fullQ, at), queue.Plan(cfg, tailQ, at)) || !reflect.DeepEqual(fullS.Hold(f.alias), tailS.Hold(f.alias)) {
					t.Fatalf("depth %d cut %d revision %d diverged", depth, cut, i)
				}
				if i < len(steps) {
					fullQ, fullS = replayAt(t, fullQ, fullS, i, steps[i])
					tailQ, tailS = replayAt(t, tailQ, tailS, i, steps[i])
				}
			}
		}
	}
}
