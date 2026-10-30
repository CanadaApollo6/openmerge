package status

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

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
