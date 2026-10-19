package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestIdentityASCIIAgreement(t *testing.T) {
	const queue = "queues:\n- name: main\n  merge_when: [check-success = ci]\n  approvals:\n    default: {group: reviewers}\n"
	for _, name := range []string{"ſam", "Kam", "Ａam", "ıam", "İam"} {
		for _, tc := range []struct {
			name, policy, path string
			line               int
		}{
			{"member-login", "groups: {reviewers: [NAME]}\n" + queue, "groups.reviewers[0]", 2},
			{"member-org", "groups: {reviewers: [NAME/team]}\n" + queue, "groups.reviewers[0]", 2},
			{"member-team", "groups: {reviewers: [org/NAME]}\n" + queue, "groups.reviewers[0]", 2},
			{"agent", "groups: {reviewers: [sam]}\nagents: [{identity: NAME, accountable: [sam]}]\n" + queue, "agents[0].identity", 3},
			{"accountable", "groups: {reviewers: [sam]}\nagents: [{identity: robot, accountable: [NAME]}]\n" + queue, "agents[0].accountable[0]", 3},
			{"owner-user", "groups: {reviewers: [sam]}\n" + queue + "    rules:\n    - when: {labels: ['owned:user:NAME']}\n      require: {group: reviewers}\n", "queues[0].approvals.rules[0].when.labels[0]", 9},
			{"owner-org", "groups: {reviewers: [sam]}\n" + queue + "    rules:\n    - when: {labels: ['owned:team:NAME/team']}\n      require: {group: reviewers}\n", "queues[0].approvals.rules[0].when.labels[0]", 9},
			{"owner-team", "groups: {reviewers: [sam]}\n" + queue + "    rules:\n    - when: {labels: ['owned:team:org/NAME']}\n      require: {group: reviewers}\n", "queues[0].approvals.rules[0].when.labels[0]", 9},
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				data := []byte("version: 1\n" + strings.ReplaceAll(tc.policy, "NAME", name))
				_, _, err := Load(data)
				if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("line %d: %s:", tc.line, tc.path)) {
					t.Fatalf("missing original location: %v", err)
				}
				assertSchemaAgreement(t, data, err)
				// Portable patterns alone must reject it, without relying on custom formats.
				value, e := schemaInput(data)
				if e != nil {
					t.Fatal(e)
				}
				doc, e := jsonschema.UnmarshalJSON(strings.NewReader(schema.V1))
				if e != nil {
					t.Fatal(e)
				}
				compiler := jsonschema.NewCompiler()
				if e = compiler.AddResource("https://example.test/schema", doc); e != nil {
					t.Fatal(e)
				}
				compiled, e := compiler.Compile("https://example.test/schema")
				if e != nil {
					t.Fatal(e)
				}
				if e = compiled.Validate(value); e == nil {
					t.Fatal("schema accepted Unicode identity")
				}
			})
		}
	}
}

func TestIdentityASCIILegacySpellings(t *testing.T) {
	data := []byte(`version: 1
groups: {reviewers: [Sam, Org/Team_Name, 'coder[BOT]']}
agents: [{identity: 'Robot[BOT]', accountable: [Sam]}]
queues:
- name: main
  merge_when: [check-success = ci]
  approvals:
    default: {group: reviewers}
    rules:
    - when: {labels: ['owned:user:sam', 'owned:team:org/team_name']}
      require: {group: reviewers}
`)
	_, _, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaAgreement(t, data, err)
}
