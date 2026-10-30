package status

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/CanadaApollo6/openmerge/internal/audit"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/journalreader"
)

// TestDependencyExportAudiences exports every dependency record with journal
// metadata only to a complete audience. A denied, unbound or foreign grant
// falls back to the redacted consumer-repository projection.
func TestDependencyExportAudiences(t *testing.T) {
	j, binding, ref, grant := statusfixture.StatusJournal(t, "dependency-waiting", 2)
	sources := statusfixture.StatusSources()
	scope, entries, err := DependencyExport(j, binding, map[string][]dependency.Grant{ref: {grant}}, sources)
	if err != nil || scope != audit.DependencyAudienceComplete {
		t.Fatal(scope, err)
	}
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Kind)
		record := j.Events[e.Source.ID-1]
		if !reflect.DeepEqual(e.Evidence, json.RawMessage(record.Payload)) || e.Source.RecordedAt != record.RecordedAt.Format("2006-01-02T15:04:05.000000Z") {
			t.Fatalf("complete record %+v does not match journal row %d", e, record.ID)
		}
	}
	if want := []string{"journal", "policy", "declaration", "observation", "diagnostic", "observation", "diagnostic"}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	key := audit.Key{Installation: j.Key.Installation, Repository: j.Key.Repository, Base: j.Key.BaseBranch}
	dir := filepath.Join(t.TempDir(), "export")
	if _, err := audit.WriteDependencyExport(dir, key, scope, entries); err != nil {
		t.Fatal(err)
	}
	m, records, err := audit.VerifyDependencyExport(dir)
	if err != nil || m.Audience != scope || len(records) != len(entries) {
		t.Fatal(m, err)
	}
	for _, r := range records {
		if _, err := journalreader.DecodeEvent(r.Evidence); err != nil {
			t.Fatalf("complete evidence is not a strict journal record: %v", err)
		}
	}
	denied := grant
	denied.Allowed = false
	foreign := grant
	foreign.RepositoryID = "other"
	for name, tc := range map[string]struct {
		audience map[string][]dependency.Grant
		sources  map[string]dependency.SourceRecord
	}{
		"none":    {nil, sources},
		"denied":  {map[string][]dependency.Grant{ref: {grant, denied}}, sources},
		"foreign": {map[string][]dependency.Grant{ref: {foreign}}, sources},
		"unbound": {map[string][]dependency.Grant{ref: {grant}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			scope, entries, err := DependencyExport(j, binding, tc.audience, tc.sources)
			if err != nil || scope != audit.DependencyAudienceRedacted || len(entries) != 2 || entries[0].Kind != "policy" || entries[1].Kind != "declaration" {
				t.Fatalf("%s %+v %v", scope, entries, err)
			}
			manifest, stream, err := DependencyExportBytes(j, binding, tc.audience, tc.sources)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := audit.DecodeDependencyExport(manifest, stream); err != nil || strings.Contains(string(stream), ref) || strings.Contains(string(stream), `"id"`) {
				t.Fatalf("redacted stream %s: %v", stream, err)
			}
		})
	}
}

// redactedEvidenceLeaves names the only input paths a redacted export record
// may carry. Every string value in the projection must come from one of them.
var redactedEvidenceLeaves = map[string][]string{
	"declaration":          {".ChangeID", ".Hold.Editors[]"},
	"policy":               {".Commit", ".Digest"},
	"acknowledgement":      {".Writer.SubjectID", ".Exclusions[]", ".GoverningCommit", ".PolicyDigest"},
	"self_acknowledgement": {".Acknowledged.Writer.SubjectID", ".Overridden[]", ".Acknowledged.GoverningCommit", ".Acknowledged.PolicyDigest"},
}

func jsonStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, item := range x {
			jsonStrings(item, out)
		}
	case map[string]any:
		for _, item := range x {
			jsonStrings(item, out)
		}
	}
}

// TestDependencyExportReflectAllowlist fills every field of every dependency
// event, including unexported and nested ones, and requires the redacted
// projection to carry only allowlisted inputs. Kinds without a projection,
// including future kinds, are withheld.
func TestDependencyExportReflectAllowlist(t *testing.T) {
	events := map[string]dependency.Event{
		"declaration": &dependency.DependenciesDeclared{}, "policy": &dependency.PolicyApplied{},
		"acknowledgement": &dependency.Acknowledged{}, "self_acknowledgement": &dependency.SelfAcknowledged{},
		"journal": &dependency.JournalInitialized{}, "observation": &dependency.Observed{},
		"diagnostic": &dependency.DiagnosticRecorded{}, "composition": &dependency.CompositionRecorded{},
		"decision": &dependency.DecisionRecorded{},
	}
	for kind, ptr := range events {
		t.Run(kind, func(t *testing.T) {
			v := reflect.ValueOf(ptr).Elem()
			reflectFill(t, v, "", map[reflect.Type]int{})
			entry, ok := redactedEntry(kind, v.Interface().(dependency.Event), dependency.State{}, dependency.PolicyApplied{})
			allowed, projected := redactedEvidenceLeaves[kind]
			if ok != projected {
				t.Fatalf("projection presence %t for %s", ok, kind)
			}
			if !ok {
				return
			}
			raw, err := json.Marshal(entry.Evidence)
			if err != nil {
				t.Fatal(err)
			}
			var tree any
			if err := json.Unmarshal(raw, &tree); err != nil {
				t.Fatal(err)
			}
			var values []string
			jsonStrings(tree, &values)
			seen := map[string]bool{}
			for _, value := range values {
				path, filled := strings.CutPrefix(value, "filled:")
				if value != "" && (!filled || !slices.Contains(allowed, path)) {
					t.Fatalf("%s projects non-allowlisted %q: %s", kind, value, raw)
				}
				seen[path] = filled
			}
			for _, path := range allowed {
				if !seen[path] {
					t.Fatalf("%s lost allowlisted %s: %s", kind, path, raw)
				}
			}
		})
	}
}

// redactedEvidenceInputs names every input a redacted projection may depend on:
// scalar leaves, slice elements ("[]") and slice lengths (no suffix). Paths
// under "policy" are the governing companion argument.
var redactedEvidenceInputs = map[string][]string{
	"declaration":          {".ChangeID", ".Revision", ".Hold.Held", ".Hold.Unknown", ".Hold.Editors", ".Hold.Editors[]", ".Redeclaration"},
	"policy":               {".Enabled", ".Policy.SingleMaintainer", ".Commit", ".Digest", ".Writers"},
	"acknowledgement":      {".Writer.SubjectID", ".Exclusions", ".Exclusions[]", ".GoverningCommit", ".PolicyDigest"},
	"self_acknowledgement": {".Acknowledged.Writer.SubjectID", ".Overridden", ".Overridden[]", ".Acknowledged.GoverningCommit", ".Acknowledged.PolicyDigest", "policy.Enabled", "policy.Policy.SingleMaintainer"},
}

// inputMutation changes one input: a scalar leaf, or a slice's length.
type inputMutation struct {
	path   string
	toggle bool
	apply  func()
}

// inputMutations lists every mutation of v in a fixed order. Each scalar is
// changed to a different value of its type, and each slice is both emptied and
// grown, so derived values such as counts are covered as well as copies.
func inputMutations(t *testing.T, v reflect.Value, path string, out *[]inputMutation) {
	t.Helper()
	if !v.CanSet() {
		v = reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		*out = append(*out, inputMutation{path: path, apply: func() { v.Set(reflect.ValueOf(v.Interface().(time.Time).Add(time.Hour))) }})
		return
	}
	switch v.Kind() {
	case reflect.String:
		*out = append(*out, inputMutation{path: path, apply: func() { v.SetString(v.String() + "~") }})
	case reflect.Bool:
		*out = append(*out, inputMutation{path: path, toggle: true, apply: func() { v.SetBool(!v.Bool()) }})
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*out = append(*out, inputMutation{path: path, apply: func() { v.SetInt(v.Int() + 1) }})
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*out = append(*out, inputMutation{path: path, apply: func() { v.SetUint(v.Uint() + 1) }})
	case reflect.Float32, reflect.Float64:
		*out = append(*out, inputMutation{path: path, apply: func() { v.SetFloat(v.Float() + 1) }})
	case reflect.Pointer:
		if !v.IsNil() {
			inputMutations(t, v.Elem(), path, out)
		}
	case reflect.Slice:
		*out = append(*out, inputMutation{path: path, apply: func() { v.Set(reflect.Zero(v.Type())) }})
		if v.Len() > 0 {
			*out = append(*out, inputMutation{path: path, apply: func() { v.Set(reflect.Append(v, v.Index(0))) }})
		}
		for i := range v.Len() {
			inputMutations(t, v.Index(i), path+"[]", out)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			inputMutations(t, v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	default:
		t.Fatalf("%s: cannot mutate %s", path, v.Type())
	}
}

// TestDependencyExportReflectInputs fills every input of every redacted
// projection, then changes each one alone, whatever its type. An input off the
// allowlist must leave the projected JSON byte-identical; an allowlisted one
// must change it. Every boolean is also cleared first, so a projection that
// derives an allowlisted value from another input is caught in either state.
func TestDependencyExportReflectInputs(t *testing.T) {
	events := map[string]func() dependency.Event{
		"declaration":          func() dependency.Event { return &dependency.DependenciesDeclared{} },
		"policy":               func() dependency.Event { return &dependency.PolicyApplied{} },
		"acknowledgement":      func() dependency.Event { return &dependency.Acknowledged{} },
		"self_acknowledgement": func() dependency.Event { return &dependency.SelfAcknowledged{} },
	}
	for kind, allowed := range redactedEvidenceInputs {
		t.Run(kind, func(t *testing.T) {
			// build returns filled inputs and their mutations, event first.
			build := func() (dependency.Event, *dependency.PolicyApplied, []inputMutation) {
				ptr, policy := events[kind](), &dependency.PolicyApplied{}
				reflectFill(t, reflect.ValueOf(ptr).Elem(), "", map[reflect.Type]int{})
				reflectFill(t, reflect.ValueOf(policy).Elem(), "policy", map[reflect.Type]int{})
				var mutations []inputMutation
				inputMutations(t, reflect.ValueOf(ptr).Elem(), "", &mutations)
				inputMutations(t, reflect.ValueOf(policy).Elem(), "policy", &mutations)
				return ptr, policy, mutations
			}
			project := func(event dependency.Event, policy *dependency.PolicyApplied) []byte {
				entry, ok := redactedEntry(kind, reflect.ValueOf(event).Elem().Interface().(dependency.Event), dependency.State{}, *policy)
				if !ok {
					t.Fatalf("%s not projected", kind)
				}
				raw, err := json.Marshal(entry.Evidence)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			_, _, mutations := build()
			seen := map[string]bool{}
			for _, cleared := range []bool{false, true} {
				// prepare clears every boolean except the one under test.
				prepare := func(skip int) (dependency.Event, *dependency.PolicyApplied, []inputMutation) {
					event, policy, ms := build()
					for i, m := range ms {
						if cleared && m.toggle && i != skip {
							m.apply()
						}
					}
					return event, policy, ms
				}
				event, policy, _ := prepare(-1)
				baseline := project(event, policy)
				for i, m := range mutations {
					event, policy, ms := prepare(i)
					if !cleared || !m.toggle {
						ms[i].apply()
					}
					got := project(event, policy)
					seen[m.path] = true
					switch {
					case !slices.Contains(allowed, m.path) && !bytes.Equal(got, baseline):
						t.Fatalf("non-allowlisted input %s changed the projection (cleared=%t):\n%s\n%s", m.path, cleared, baseline, got)
					case slices.Contains(allowed, m.path) && !cleared && bytes.Equal(got, baseline):
						t.Fatalf("allowlisted input %s does not reach the projection: %s", m.path, got)
					}
				}
			}
			for _, path := range allowed {
				if !seen[path] {
					t.Fatalf("allowlisted input %s is not an input", path)
				}
			}
		})
	}
}
