package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

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

func TestStrictEventShape(t *testing.T) {
	for name, e := range goldenEvents(t) {
		t.Run(name, func(t *testing.T) {
			kind, raw, err := EncodeEvent(e)
			if err != nil {
				t.Fatal(err)
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
			for _, bad := range [][]byte{
				bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
				bytes.Replace(raw, []byte(`"request":{`), []byte(`"request":{},"request":{`), 1),
				bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":2`), 1),
				bytes.Replace(raw, []byte(kind), []byte("future-provenance"), 1),
				append(append([]byte{}, raw...), []byte(`{}`)...),
			} {
				if _, err := DecodeEvent(bad); err == nil {
					t.Fatal("accepted corrupt envelope")
				}
			}
		})
	}
	if _, err := DecodeEvent([]byte{'{', '"', 0xff, '"', ':', '0', '}'}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := DecodeEvent(bytes.Repeat([]byte(" "), maxEventBytes+1)); err == nil {
		t.Fatal("unbounded event")
	}
	for _, e := range []Event{nil, (*Observation)(nil), (*Landing)(nil)} {
		if _, _, err := EncodeEvent(e); err == nil {
			t.Fatal("accepted nil event")
		}
	}
}
func TestStrictSnapshotShapeAndBindings(t *testing.T) {
	h, _ := fixture(t)
	base := []byte(`{"Version":6}`)
	s := apply(t, State{}, 3, h)
	raw := snapshotBytes(t, base, s)
	decode := func(string, []byte) error { return nil }
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	for _, path := range jsonFields(tree, nil) {
		if path[0] == "Base" {
			continue
		}
		for _, mode := range []string{"missing", "unknown", "null", "case"} {
			if _, err := DecodeSnapshot(mutateField(t, raw, path, mode), decode); err == nil {
				t.Fatalf("accepted %s at %v", mode, path)
			}
		}
	}
	for name, mutate := range map[string]func(*snapshot){
		"last id":        func(s *snapshot) { s.LastID++ },
		"duplicates":     func(s *snapshot) { s.Records = append(s.Records, s.Records[0]) },
		"nested v9":      func(s *snapshot) { s.Base = raw },
		"unknown base":   func(s *snapshot) { s.Base = []byte(`{"Version":10}`) },
		"empty evidence": func(s *snapshot) { s.Records = []Record{} },
	} {
		t.Run(name, func(t *testing.T) {
			var v snapshot
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
			mutate(&v)
			bad, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSnapshot(bad, decode); err == nil {
				t.Fatal("accepted corrupt snapshot")
			}
		})
	}
	if _, err := DecodeSnapshot(bytes.Repeat([]byte(" "), maxSnapshotBytes+1), decode); err == nil {
		t.Fatal("unbounded snapshot")
	}
	if _, err := DecodeSnapshot(raw, nil); err == nil {
		t.Fatal("missing base validation")
	}
	if _, err := DecodeSnapshot(raw, func(string, []byte) error { return fmt.Errorf("bad base") }); err == nil {
		t.Fatal("base error ignored")
	}
}
