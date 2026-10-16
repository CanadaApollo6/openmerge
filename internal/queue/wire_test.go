package queue

import (
	"bytes"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func wireFixtures(t *testing.T, action bool) []string {
	t.Helper()
	prefix := "event"
	if action {
		prefix = "action"
	}
	paths, err := filepath.Glob("testdata/wire/v1/" + prefix + "_*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("wire fixtures: %v (%d found)", err, len(paths))
	}
	return paths
}

func fixtureVariantNames(t *testing.T, action bool) map[string]bool {
	t.Helper()
	names := make(map[string]bool)
	for _, path := range wireFixtures(t, action) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if action {
			value, err = DecodeAction(data)
		} else {
			value, err = DecodeEvent(data)
		}
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		name := reflect.TypeOf(value).Name()
		if names[name] {
			t.Fatalf("duplicate fixture for %s", name)
		}
		names[name] = true
	}
	return names
}

func TestWireV1Golden(t *testing.T) {
	for _, action := range []bool{false, true} {
		for _, path := range wireFixtures(t, action) {
			t.Run(filepath.Base(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var encoded []byte
				if action {
					value, err := DecodeAction(data)
					if err != nil {
						t.Fatal(err)
					}
					_, encoded, err = EncodeAction(value)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					value, err := DecodeEvent(data)
					if err != nil {
						t.Fatal(err)
					}
					_, encoded, err = EncodeEvent(value)
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(data, encoded) {
					t.Fatalf("wire v1 changed:\n got %s\nwant %s", encoded, data)
				}
			})
		}
	}
}

func TestWirePrivateKeyInputs(t *testing.T) {
	samples := []Action{
		RefreshChecks{BatchID: "b1", TestedSHA: "tested", epoch: 4},
		Dequeue{PR: 2, HeadSHA: "head", AdmissionID: 3, Reason: Requested, BatchID: "b1", scope: "batch/b1"},
		ReportStatus{PR: 2, HeadSHA: "head", Status: "dequeued", Reason: "requested", Detail: "Persisted wording: punctuation stays!", revision: 7, admission: 3},
	}
	for _, sample := range samples {
		_, data, err := EncodeAction(sample)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeAction(data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, sample) || decoded.Key() != sample.Key() {
			t.Fatalf("lost key inputs: %#v / %#v", decoded, sample)
		}
	}
}

func TestWireRejectsInvalidData(t *testing.T) {
	for _, data := range []string{
		`{`, `{"version":2,"type":"pr_closed","data":{"pr":2}}`,
		`{"type":"pr_closed","data":{"pr":2}}`,
		`{"version":1,"type":"future","data":{}}`,
		`{"version":1,"type":"pr_closed","data":{"pr":"bad"}}`,
		`{"version":1,"type":"refresh_checks","data":{"epoch":"bad"}}`,
		`{"version":1,"type":"resume_requested","data":null}`,
		`{"version":1,"type":"resume_requested"}`,
		`{"version":1,"type":"resume_requested","data":{},"extra":1}`,
		`{"version":1,"type":"resume_requested","data":{}} {}`,
	} {
		if _, err := DecodeEvent([]byte(data)); err == nil {
			t.Errorf("accepted event %s", data)
		}
		if _, err := DecodeAction([]byte(data)); err == nil {
			t.Errorf("accepted action %s", data)
		}
	}
	if _, _, err := EncodeEvent(nil); err == nil {
		t.Fatal("accepted nil event")
	}
	if _, _, err := EncodeAction(nil); err == nil {
		t.Fatal("accepted nil action")
	}
}

// Type checking enumerates concrete implementations, including implementations
// inherited through embedding. A new queue variant fails until registered here.
func TestWireRegistryCoversQueueVariants(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	cfg := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := cfg.Check("github.com/CanadaApollo6/openmerge/internal/queue", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		registered map[string]bool
	}{
		{"Event", fixtureVariantNames(t, false)},
		{"Action", fixtureVariantNames(t, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iface := pkg.Scope().Lookup(tc.name).Type().Underlying().(*types.Interface)
			found := make(map[string]bool)
			for _, name := range pkg.Scope().Names() {
				obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
				if !ok || obj.IsAlias() {
					continue
				}
				if _, ok := obj.Type().Underlying().(*types.Interface); ok {
					continue
				}
				if types.Implements(obj.Type(), iface) || types.Implements(types.NewPointer(obj.Type()), iface) {
					found[name] = true
					if !tc.registered[name] {
						t.Errorf("queue.%s implements %s but has no wire mapping and golden fixture", name, tc.name)
					}
				}
			}
			if !reflect.DeepEqual(found, tc.registered) {
				t.Errorf("registry mismatch: queue %v, wire %v", found, tc.registered)
			}
		})
	}
}
