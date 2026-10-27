package provenance

import (
	"fmt"
	"os"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

const candidatePrefix = "version: 1\ngroups: {security: [bob]}\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n  approvals: {default: any_writer}\n  provenance: "

func candidateSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("testdata/provenance.candidate.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.RegisterFormat(&jsonschema.Format{Name: "provenance-condition", Validate: func(v any) error {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		_, err := ParseCondition(s)
		return err
	}})
	if err := compiler.AddResource("candidate.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestCandidateSchemaAgreementAndPublicRejection(t *testing.T) {
	schema := candidateSchema(t)
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"human approvals", `{requirements: [{when: [provenance = agent], require: {approvals: {human: true, count: 2}}}]}`, true},
		{"resolver conditions candidate only", `{requirements: [{when: ["#agent-commits > 0"], require: {conditions: [checkpoint-present]}}]}`, true},
		{"all logical forms", `{requirements: [{when: {or: [provenance = mixed, {not: {and: [provenance = human, draft]}}]}, require: {conditions: [check-success = ci], approvals: {group: security, count: 1}}}]}`, true},
		{"code owners", `{requirements: [{when: provenance = undeclared, require: {approvals: {code_owners: true, receipt: true}}}]}`, true},
		{"any writer", `{requirements: [{when: [], require: {approvals: any_writer}}]}`, true},
		{"empty additions", `{}`, false},
		{"empty requirements", `{requirements: []}`, false},
		{"null requirements", `{requirements: null}`, false},
		{"missing when", `{requirements: [{require: {conditions: []}}]}`, false},
		{"missing require", `{requirements: [{when: []}]}`, false},
		{"empty require", `{requirements: [{when: [], require: {}}]}`, false},
		{"none", `{requirements: [{when: [], require: {approvals: none}}]}`, false},
		{"count zero", `{requirements: [{when: [], require: {approvals: {human: true, count: 0}}}]}`, false},
		{"count quoted", `{requirements: [{when: [], require: {approvals: {human: true, count: '2'}}}]}`, false},
		{"human false", `{requirements: [{when: [], require: {approvals: {human: false}}}]}`, false},
		{"conflicting selectors", `{requirements: [{when: [], require: {approvals: {group: security, any_writer: true}}}]}`, false},
		{"unknown require key", `{requirements: [{when: [], require: {conditions: [], waive: true}}]}`, false},
		{"unknown outer key", `{requirements: [], enabled: true}`, false},
		{"invalid class", `{requirements: [{when: provenance = verified-human, require: {conditions: []}}]}`, false},
		{"wrong type", `{requirements: [{when: 1, require: {conditions: []}}]}`, false},
		{"two logical operators", `{requirements: [{when: {and: [], or: []}, require: {conditions: []}}]}`, false},
		{"wrong group type", `{requirements: [{when: [], require: {conditions: {or: draft}}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, err := LoadCandidate([]byte(candidatePrefix + tc.value))
			if (err == nil) != tc.valid {
				t.Fatalf("parser err=%v want valid=%v", err, tc.valid)
			}
			var raw any
			if err := yaml.Unmarshal([]byte(tc.value), &raw); err != nil {
				t.Fatal(err)
			}
			if schemaErr := schema.Validate(raw); (schemaErr == nil) != tc.valid {
				t.Fatalf("schema err=%v want valid=%v", schemaErr, tc.valid)
			}
			if _, _, err := config.Load([]byte(candidatePrefix + tc.value)); err == nil {
				t.Fatal("public loader accepted provenance")
			}
			if tc.valid && len(candidate.Requirements["main"]) != 1 {
				t.Fatal(candidate)
			}
		})
	}
}

func TestCandidateContextAndYAMLSafety(t *testing.T) {
	good := `{requirements: [{when: [provenance = agent], require: {approvals: {human: true}}}]}`
	for _, source := range []string{
		strings.Replace(candidatePrefix+good, "  approvals: {default: any_writer}\n", "", 1),
		candidatePrefix + strings.Replace(good, "human: true", "group: missing", 1),
		candidatePrefix + `{requirements: [{when: [], when: [], require: {conditions: []}}]}`,
		candidatePrefix + `{requirements: &r [{when: *r, require: {conditions: []}}]}`,
		candidatePrefix + `!!map {requirements: []}`,
		candidatePrefix + good + "\n---\nversion: 1",
		candidatePrefix + "{requirements: [{when: " + strings.Repeat("{not: ", 70) + "draft" + strings.Repeat("}", 70) + ", require: {conditions: []}}]}",
	} {
		if _, err := LoadCandidate([]byte(source)); err == nil {
			t.Fatal("invalid candidate accepted", source)
		}
	}
	candidate, err := LoadCandidate([]byte(candidatePrefix + good))
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Requirements["main"][0].Approvals.Selector != cp.Human {
		t.Fatal(candidate)
	}
	legacy := strings.Replace(candidatePrefix, "  provenance: ", "", 1)
	without, _, err := config.Load([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Config.Queues[0].Digest(true) != without.Queues[0].Digest(true) {
		t.Fatal("candidate changed baseline digest")
	}
}
