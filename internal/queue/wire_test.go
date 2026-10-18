package queue

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

func TestWireEventKeysLowercaseSnakeCase(t *testing.T) {
	// These exact pre-existing v1 paths are frozen by TestWireV1Golden. They
	// cannot be renamed without changing existing journal bytes.
	legacy := map[string]bool{
		"event_change_accountability_observed.json.data.actor.ID":                                  true,
		"event_change_accountability_observed.json.data.actor.Kind":                                true,
		"event_change_accountability_observed.json.data.actor.Login":                               true,
		"event_change_accountability_observed.json.data.author.ID":                                 true,
		"event_change_accountability_observed.json.data.author.Kind":                               true,
		"event_change_accountability_observed.json.data.author.Login":                              true,
		"event_change_accountability_observed.json.data.humans[].ID":                               true,
		"event_change_accountability_observed.json.data.humans[].Kind":                             true,
		"event_change_accountability_observed.json.data.humans[].Login":                            true,
		"event_change_classified.json.data.evidence.Classification":                                true,
		"event_change_classified.json.data.evidence.Classification.CodeOwners":                     true,
		"event_change_classified.json.data.evidence.Classification.CodeOwnersKnown":                true,
		"event_change_classified.json.data.evidence.Classification.Facts":                          true,
		"event_change_classified.json.data.evidence.Classification.Files":                          true,
		"event_change_classified.json.data.evidence.Classification.Files[].Binary":                 true,
		"event_change_classified.json.data.evidence.Classification.Files[].Facts":                  true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks":                  true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Complete":         true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks":            true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks[].NewLines": true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks[].NewStart": true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks[].OldLines": true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks[].OldStart": true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Hunks[].Text":     true,
		"event_change_classified.json.data.evidence.Classification.Files[].Hunks.Omitted":          true,
		"event_change_classified.json.data.evidence.Classification.Files[].NewBlob":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].NewMode":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].NewPath":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].OldBlob":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].OldMode":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].OldPath":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].Reasons":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].RenamePaired":           true,
		"event_change_classified.json.data.evidence.Classification.Files[].Renamed":                true,
		"event_change_classified.json.data.evidence.Classification.Files[].Submodule":              true,
		"event_change_classified.json.data.evidence.Classification.HunksComplete":                  true,
		"event_change_classified.json.data.evidence.Classification.Inputs":                         true,
		"event_change_classified.json.data.evidence.Classification.Inputs.AttributeBlobs":          true,
		"event_change_classified.json.data.evidence.Classification.Inputs.Base":                    true,
		"event_change_classified.json.data.evidence.Classification.Inputs.ChangeID":                true,
		"event_change_classified.json.data.evidence.Classification.Inputs.ClassifierVersion":       true,
		"event_change_classified.json.data.evidence.Classification.Inputs.ConfigBlob":              true,
		"event_change_classified.json.data.evidence.Classification.Inputs.Generation":              true,
		"event_change_classified.json.data.evidence.Classification.Inputs.GoverningCommit":         true,
		"event_change_classified.json.data.evidence.Classification.Inputs.Head":                    true,
		"event_change_classified.json.data.evidence.Classification.Inputs.MergeBase":               true,
		"event_change_classified.json.data.evidence.Classification.Inputs.OwnersBlob":              true,
		"event_change_classified.json.data.evidence.Classification.Inputs.PolicyDigest":            true,
		"event_change_classified.json.data.evidence.Classification.Inputs.Revision":                true,
		"event_change_classified.json.data.evidence.Classification.InventoryComplete":              true,
		"event_change_classified.json.data.evidence.Classification.Paths":                          true,
		"event_change_classified.json.data.evidence.Classification.PathsSafe":                      true,
		"event_change_classified.json.data.evidence.Classification.PolicyOwners":                   true,
		"event_change_classified.json.data.evidence.Classification.PolicyOwnersKnown":              true,
		"event_change_classified.json.data.evidence.Classification.Reasons":                        true,
		"event_change_classified.json.data.evidence.Classification.Reasons[].Label":                true,
		"event_change_classified.json.data.evidence.Classification.Reasons[].Reasons":              true,
		"event_change_classified.json.data.evidence.Governing":                                     true,
		"event_change_classified.json.data.evidence.Governing.Attributes":                          true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Content":                true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Coverage":               true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Coverage.Completeness":  true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Coverage.Reasons":       true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Mode":                   true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].ObjectID":               true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Path":                   true,
		"event_change_classified.json.data.evidence.Governing.Attributes[].Present":                true,
		"event_change_classified.json.data.evidence.Governing.Codeowners":                          true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Content":                true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Coverage":               true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Coverage.Completeness":  true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Coverage.Reasons":       true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Mode":                   true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].ObjectID":               true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Path":                   true,
		"event_change_classified.json.data.evidence.Governing.Codeowners[].Present":                true,
		"event_change_classified.json.data.evidence.Governing.CommitSHA":                           true,
		"event_change_classified.json.data.evidence.Governing.Config":                              true,
		"event_change_classified.json.data.evidence.Governing.Config[].Content":                    true,
		"event_change_classified.json.data.evidence.Governing.Config[].Coverage":                   true,
		"event_change_classified.json.data.evidence.Governing.Config[].Coverage.Completeness":      true,
		"event_change_classified.json.data.evidence.Governing.Config[].Coverage.Reasons":           true,
		"event_change_classified.json.data.evidence.Governing.Config[].Mode":                       true,
		"event_change_classified.json.data.evidence.Governing.Config[].ObjectID":                   true,
		"event_change_classified.json.data.evidence.Governing.Config[].Path":                       true,
		"event_change_classified.json.data.evidence.Governing.Config[].Present":                    true,
		"event_change_classified.json.data.evidence.Governing.Coverage":                            true,
		"event_change_classified.json.data.evidence.Governing.Coverage.Completeness":               true,
		"event_change_classified.json.data.evidence.Governing.Coverage.Reasons":                    true,
		"event_change_classified.json.data.evidence.Governing.SelectedCodeowners":                  true,
		"event_change_classified.json.data.evidence.Governing.Semantics":                           true,
		"event_change_classified.json.data.evidence.Hunks":                                         true,
		"event_change_classified.json.data.evidence.Hunks.Completeness":                            true,
		"event_change_classified.json.data.evidence.Hunks.Reasons":                                 true,
		"event_change_classified.json.data.evidence.InputFingerprint":                              true,
		"event_change_classified.json.data.evidence.Inventory":                                     true,
		"event_change_classified.json.data.evidence.Inventory.Completeness":                        true,
		"event_change_classified.json.data.evidence.Inventory.Reasons":                             true,
		"event_review_stance_observed.json.data.observation.ObservedAt":                            true,
		"event_review_stance_observed.json.data.observation.ResponseDate":                          true,
		"event_review_stance_observed.json.data.original_submission.Precision":                     true,
		"event_review_stance_observed.json.data.original_submission.Source":                        true,
		"event_review_stance_observed.json.data.original_submission.Time":                          true,
		"event_review_stance_observed.json.data.original_submission.Uncertainty":                   true,
		"event_review_stance_observed.json.data.original_submission.UpperBound":                    true,
		"event_review_stance_observed.json.data.reviewer.ID":                                       true,
		"event_review_stance_observed.json.data.reviewer.Kind":                                     true,
		"event_review_stance_observed.json.data.reviewer.Login":                                    true,
		"event_review_stance_observed.json.data.source_time.Precision":                             true,
		"event_review_stance_observed.json.data.source_time.Source":                                true,
		"event_review_stance_observed.json.data.source_time.Time":                                  true,
		"event_review_stance_observed.json.data.source_time.Uncertainty":                           true,
		"event_review_stance_observed.json.data.source_time.UpperBound":                            true,
		"event_review_stance_observed.json.data.stance.Head":                                       true,
		"event_review_stance_observed.json.data.stance.Kind":                                       true,
		"event_review_stance_observed.json.data.stance.Receipt":                                    true,
		"event_review_stance_observed.json.data.stance.Revision":                                   true,
		"event_review_stance_observed.json.data.stance.Source":                                     true,
		"event_review_stance_observed.json.data.stance.Source.ActingUserID":                        true,
		"event_review_stance_observed.json.data.stance.Source.Channel":                             true,
		"event_review_stance_observed.json.data.stance.Source.DismissedReviewID":                   true,
		"event_review_stance_observed.json.data.stance.Source.ID":                                  true,
		"event_review_stance_observed.json.data.stance.Source.Precision":                           true,
		"event_review_stance_observed.json.data.stance.Source.Sequence":                            true,
		"event_review_stance_observed.json.data.stance.Source.Time":                                true,
		"event_review_stance_observed.json.data.stance.Source.TimeKnown":                           true,
		"event_review_stance_observed.json.data.stance.Source.UpperTimeBound":                      true,
		"event_review_stance_observed.json.data.stance.UserID":                                     true,
	}
	keyPattern := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	var check func(any, string)
	check = func(value any, path string) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if !keyPattern.MatchString(key) && !legacy[path+"."+key] {
					t.Errorf("%s: non-snake-case key %q", path, key)
				}
				check(child, path+"."+key)
			}
		case []any:
			for _, child := range value {
				check(child, path+"[]")
			}
		}
	}
	for _, path := range wireFixtures(t, false) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		event, err := DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		_, raw, err = EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		check(value, filepath.Base(path))
	}
}
