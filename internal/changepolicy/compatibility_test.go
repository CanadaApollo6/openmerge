package changepolicy_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func TestLegacyDigestAndMixedRepository(t *testing.T) {
	const source = "version: 1\nqueues:\n- name: default\n  merge_when: [check-success = ci]\n"
	for _, tc := range []struct{ extra, want string }{
		{"", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"  speculation: {max_depth: 1, min_depth: 1}\n", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"  batch: {max_size: 8, max_wait: 5m}\n  checks: {retry_failed: 1}\n", "936a86848e84606ba89a7ae1ee56db20d204957d800189accb6a83ea9aa505e8"},
	} {
		cfg, _, err := config.Load([]byte(source + tc.extra))
		if err != nil {
			t.Fatal(err)
		}
		got, err := changepolicy.WithPolicyDigest(cfg.Queues[0].Digest(true), "")
		if err != nil || got != tc.want {
			t.Fatalf("legacy digest %s, %v", got, err)
		}
	}
	t.Run("mixed-repository", func(t *testing.T) {
		r, _, err := changepolicy.ParseCandidate([]byte("classification: {docs: ['**/*.md']}\nqueues: [{name: opted, approvals: {default: any_writer}}, {name: legacy, changes: {pull_requests: required}}]"))
		if err != nil {
			t.Fatal(err)
		}
		// No resolved IDs/content are needed for a sibling without approvals.
		component, err := changepolicy.PolicyDigest(changepolicy.DigestInputs{Repository: r, Queue: r.Queues[1]})
		if err != nil || component != "" {
			t.Fatal(component, err)
		}
		cfg, _, err := config.Load([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		legacy := cfg.Queues[0].Digest(true)
		if got, err := changepolicy.WithPolicyDigest(legacy, component); err != nil || got != legacy {
			t.Fatal(got, err)
		}
		got := changepolicy.Evaluate(changepolicy.Policy{Approvals: r.Queues[1].Approvals}, changepolicy.Input{})
		if !got.Allowed || got.OptedIn || len(got.Requirements) > 0 || len(got.Reasons) > 0 {
			t.Fatalf("legacy queue observed policy facts: %+v", got)
		}
		combined, err := changepolicy.WithPolicyDigest(legacy, legacy)
		if err != nil || combined == legacy {
			t.Fatal("opt-in component did not invalidate digest", err)
		}
	})
}

func TestPublicLoaderAndSchemaStillRejectCandidateKeys(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schema.V1))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("public.json", doc); err != nil {
		t.Fatal(err)
	}
	published, err := compiler.Compile("public.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, addition := range []string{
		"classification: {docs: ['**/*.md']}\n", "groups: {platform: [alice]}\n", "agents: [{identity: 'acme-coder[bot]', accountable: [alice]}]\n",
		"  changes: {pull_requests: required}\n", "  approvals: {default: any_writer}\n",
	} {
		source := "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n" + addition
		if _, _, err := config.Load([]byte(source)); err == nil {
			t.Fatalf("public loader accepted %s", addition)
		}
		var value any
		if err := yaml.Unmarshal([]byte(source), &value); err != nil {
			t.Fatal(err)
		}
		if err := published.Validate(value); err == nil {
			t.Fatalf("public schema accepted %s", addition)
		}
	}
}

func TestFullCandidateSchemaComposition(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	fragment := read("testdata/policy.schema.json")
	candidate := read("testdata/candidate.schema.json")
	var want map[string]any
	if err := json.Unmarshal([]byte(schema.V1), &want); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"$id", "title", "$comment"} {
		want[key] = candidate[key]
	}
	props := fragment["properties"].(map[string]any)
	for _, key := range []string{"classification", "groups", "agents"} {
		want["properties"].(map[string]any)[key] = props[key]
	}
	queueProps := props["queues"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	defs := want["$defs"].(map[string]any)
	for _, key := range []string{"changes", "approvals"} {
		defs["queue"].(map[string]any)["properties"].(map[string]any)[key] = queueProps[key]
	}
	for _, key := range []string{"requirement", "match"} {
		defs[key] = fragment["$defs"].(map[string]any)[key]
	}
	if !reflect.DeepEqual(candidate, want) {
		t.Fatal("candidate schema drifted from public schema plus proposed additions")
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("candidate.json", candidate); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/policy.candidate")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := changepolicy.ParseCandidate(data); err != nil {
		t.Fatal(err)
	}
	var full map[string]any
	if err := yaml.Unmarshal(data, &full); err != nil {
		t.Fatal(err)
	}
	full["version"] = 1
	for _, raw := range full["queues"].([]any) {
		raw.(map[string]any)["merge_when"] = []any{"check-success = ci"}
	}
	if err := compiled.Validate(full); err != nil {
		t.Fatal(err)
	}
	// Existing fields still go through the existing loader at composition.
	for _, key := range []string{"classification", "groups", "agents"} {
		delete(full, key)
	}
	for _, raw := range full["queues"].([]any) {
		q := raw.(map[string]any)
		delete(q, "approvals")
		delete(q, "changes")
	}
	legacy, err := yaml.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := config.Load(legacy); err != nil {
		t.Fatal(err)
	}
}
