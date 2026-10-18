package changepolicy

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func fixture(t *testing.T) Repository {
	t.Helper()
	data, err := os.ReadFile("testdata/policy.candidate")
	if err != nil {
		t.Fatal(err)
	}
	r, diagnostics, err := ParseCandidate(data)
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("%v, %v", diagnostics, err)
	}
	return r
}

func candidateSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile("testdata/policy.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	for name, validate := range map[string]func(string) bool{
		"policy-login": validLogin, "policy-member": validMember, "policy-label": validLabel,
		"policy-glob":         func(s string) bool { _, err := condition.CompileGlob(s); return err == nil },
		"openmerge-condition": func(s string) bool { _, err := condition.Parse(s); return err == nil },
	} {
		compiler.RegisterFormat(&jsonschema.Format{Name: name, Validate: func(value any) error {
			if s, ok := value.(string); ok && !validate(s) {
				return fmt.Errorf("invalid %s", name)
			}
			return nil
		}})
	}
	if err := compiler.AddResource("candidate.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestCandidateSchemaAgreement(t *testing.T) {
	schema := candidateSchema(t)
	for _, tc := range []struct {
		require string
		valid   bool
	}{
		{"none", true}, {"any_writer", true}, {"{human: true}", true}, {"{human: true, count: 2}", true},
		{"{any_writer: true, receipt: true}", true}, {"{group: platform, human: true, receipt: true, count: 2}", true},
		{"{group: platform}", true}, {"{human: true, receipt: true}", true}, {"{any_writer: true, human: true}", true},
		{"{any_writer: true, group: platform}", false}, {"{human: false}", false}, {"{receipt: true}", false},
		{"{count: 1}", false}, {"{group: ''}", false}, {"{any_writer: false}", false}, {"{human: true, receipt: false}", false},
		{"{human: true, count: 0}", false}, {"{human: true, count: -1}", false}, {"{human: true, count: '1'}", false},
		{"{human: true, count: 9223372036854775808}", false}, {"{human: true, extra: x}", false},
		{"{none: true, receipt: true}", false}, {"[any_writer, none]", false}, {"false", false},
	} {
		t.Run(tc.require, func(t *testing.T) {
			data := []byte("groups: {platform: [acme/platform]}\nqueues:\n- name: main\n  approvals:\n    default: any_writer\n    rules:\n    - when: {labels: [docs]}\n      require: " + tc.require + "\n")
			r, _, err := ParseCandidate(data)
			var value any
			schemaErr := yaml.Unmarshal(data, &value)
			if schemaErr == nil {
				schemaErr = schema.Validate(value)
			}
			if (err == nil) != tc.valid || (schemaErr == nil) != tc.valid {
				t.Fatalf("valid=%v, parser=%v schema=%v", tc.valid, err, schemaErr)
			}
			if tc.valid && r.Queues[0].Approvals.Rules[0].Require.Selector != None && r.Queues[0].Approvals.Rules[0].Require.Count < 1 {
				t.Fatal("count default missing")
			}
		})
	}
	data, err := os.ReadFile("testdata/policy.candidate")
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateInvalidInputs(t *testing.T) {
	for _, data := range []string{
		"", "{}", "queues: []", "queues: null", "queues: [{name: ''}]", "queues: [{name: 7}]",
		"queues: [{name: main}, {name: main}]", "queues: [{name: main, name: other}]",
		"queues: [{name: main, unknown: true}]", "unknown: 1\nqueues: [{name: main}]",
		"queues: [{name: main, changes: {pull_requests: never}}]", "queues: [{name: main, changes: {pull_requests: when_review_needed}}]",
		"queues: [{name: main, approvals: {}}]", "queues: [{name: main, approvals: {default: none}}]",
		"queues: [{name: main, approvals: {default: {group: missing}}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {}}]}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {}, require: none}]}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {labels: []}, require: none}]}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {labels: [forge-ready]}, require: none}]}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {authors: [bot]}, require: none}]}}]",
		"queues: [{name: main, approvals: {default: any_writer, rules: [{when: {paths: 'docs/**'}, require: none}]}}]",
		"classification: {docs: ['bad\\']}\nqueues: [{name: main}]",
		"classification: {sensitive: []}\nqueues: [{name: main}]", "groups: {'': [alice]}\nqueues: [{name: main}]",
		"groups: {platform: [acme/]}\nqueues: [{name: main}]", "groups: {platform: ['@alice']}\nqueues: [{name: main}]",
		"agents: [{identity: 'bot[bot]', accountable: []}]\nqueues: [{name: main}]",
		"agents: [{identity: 'a/b', accountable: [alice]}]\nqueues: [{name: main}]",
		"agents: [{identity: bot, accountable: [alice]}, {identity: BOT, accountable: [bob]}]\nqueues: [{name: main}]",
		"queues: [{name: main, approvals: {default: {human: true, count: 1.0}}}]",
		"queues: [{name: main, enter_when: [{not: {and: [unknown = x]}}]}]",
		"queues: [{name: main, merge_when: [{not: x, or: []}]}]",
		"queues: [{name: main}]\n---\nqueues: [{name: other}]",
		"queues: [&q {name: main}, *q]", "queues: [{name: !!str main}]",
	} {
		t.Run(data, func(t *testing.T) {
			if _, _, err := ParseCandidate([]byte(data)); err == nil {
				t.Fatalf("accepted:\n%s", data)
			}
		})
	}
}

func TestBotIdentityAndResolution(t *testing.T) {
	t.Run("bot-identity", func(t *testing.T) {
		r := fixture(t)
		if r.Agents[0].Identity != "acme-coder[bot]" {
			t.Fatal(r.Agents)
		}
		resolved := []ResolvedIdentity{{Name: "acme-coder[bot]", ID: "bot", Kind: AgentAccount}, {Name: "alice", ID: "a", Kind: HumanAccount}, {Name: "bob", ID: "b", Kind: HumanAccount}, {Name: "acme/platform", ID: "team"}}
		if err := r.ValidateIdentities(resolved); err != nil {
			t.Fatal(err)
		}
		if EffectiveKind(Identity{ID: "bot", Kind: HumanAccount}, []string{"bot"}) != AgentAccount {
			t.Fatal("mapped forge-human became human")
		}
		resolved[1].Kind = AgentAccount
		if r.ValidateIdentities(resolved) == nil {
			t.Fatal("accountable bot accepted")
		}
		resolved[1].Kind = HumanAccount
		resolved[1].ID = "bot"
		if r.ValidateIdentities(resolved) == nil {
			t.Fatal("agent alias accountable human accepted")
		}
		if r.ValidateIdentities(resolved[:1]) == nil {
			t.Fatal("missing resolution accepted")
		}
	})
}

func TestReviewConditionDiagnostics(t *testing.T) {
	data := []byte(`queues:
- name: main
  approvals: {default: any_writer}
  enter_when: [{or: [{not: {and: ["#approved-reviews-by >= 2"]}}, "label = ready"]}]
  merge_when: [{and: ["check-success = ci", {not: "approved-reviews-by = alice"}]}]
- name: legacy
  enter_when: ["approved-reviews-by = alice"]
`)
	_, diagnostics, err := ParseCandidate(data)
	if err != nil || len(diagnostics) != 2 {
		t.Fatalf("%v, %v", diagnostics, err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Line < 1 || !strings.HasPrefix(diagnostic.Path, "queues[0].") || !strings.Contains(diagnostic.Message, "explicit pools") {
			t.Fatal(diagnostic)
		}
	}
}
