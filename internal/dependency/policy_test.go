package dependency

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

func TestCandidatePolicySchemaAgreement(t *testing.T) {
	raw, err := os.ReadFile("testdata/policy.candidate.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schemaDoc any
	if err := json.Unmarshal(raw, &schemaDoc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.RegisterFormat(&jsonschema.Format{Name: "openmerge-dependency-repository", Validate: func(v any) error {
		if s, ok := v.(string); ok {
			_, err := repositoryURL(s)
			return err
		}
		return nil
	}})
	if err := compiler.AddResource("candidate.json", schemaDoc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		yaml  string
		valid bool
	}{
		{"mode: ordered", true},
		{"mode: ordered\nallow_from: []\nallow_to: []", true},
		{"mode: ordered\nallow_from: [https://github.com/acme/api]\nallow_to: [https://git.example:8443/acme/client]", true},
		{"{}", false}, {"mode: co-tested", false}, {"mode: off", false}, {"mode: ordered\nunknown: true", false},
		{"mode: ordered\nallow_from: ['*']", false}, {"mode: ordered\nallow_to: [http://github.com/acme/api]", false},
		{"mode: ordered\nallow_from: [https://github.com/acme/api, https://github.com/acme/api]", false},
		{"mode: ordered\nallow_to: [42]", false}, {"mode: ordered\nallow_from: null", false}, {"mode: ordered\nallow_to: [https://github.com/acme/api/]", false},
		{"mode: ordered\nallow_to: ['https://github.com/acme/api?x']", false},
		{"mode: ordered\nsingle_maintainer: true", true},
		{"mode: ordered\nsingle_maintainer: false", true},
		{"mode: ordered\nsingle_maintainer: 'true'", false},
		{"mode: ordered\nsingle_maintainer: null", false},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			_, err := ParsePolicyCandidate([]byte(tc.yaml))
			if (err == nil) != tc.valid {
				t.Fatalf("parser: %v", err)
			}
			var doc any
			if err := yaml.Unmarshal([]byte(tc.yaml), &doc); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(doc); (err == nil) != tc.valid {
				t.Fatalf("schema: %v", err)
			}
		})
	}
	for _, bad := range []string{"mode: ordered\nmode: ordered", "mode: ordered\nallow_from: &x []\nallow_to: *x", "mode: !!str ordered", "!!map {mode: ordered}", "mode: ordered\n---\nmode: ordered"} {
		if _, err := ParsePolicyCandidate([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestPublicConfigRejectsDependencyKeys(t *testing.T) {
	const base = "version: 1\nqueues:\n  - name: default\n    merge_when: [check-success = ci]\n"
	for _, extra := range []string{"    dependencies: {mode: ordered}\n", "    dependencies: {mode: ordered, allow_from: [], allow_to: []}\n", "    single_maintainer: true\n"} {
		if _, _, err := config.Load([]byte(base + extra)); err == nil || !strings.Contains(err.Error(), "unknown key") {
			t.Fatalf("public loader accepted candidate: %v", err)
		}
	}
	for _, key := range []string{"dependencies: {mode: ordered}", "single_maintainer: true", "allow_from: []", "allow_to: []"} {
		if _, _, err := config.Load([]byte(base + key + "\n")); err == nil {
			t.Fatalf("root %s accepted", key)
		}
	}
	// These fixed pre-dependency vectors are also guarded by internal/config.
	for _, tc := range []struct{ extra, want string }{
		{"", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"    speculation: {max_depth: 1, min_depth: 1}\n", "75c86e0f6a55101c55b9de4d07940a8d98c1f110df2b98aad901cf0557bfa4aa"},
		{"    batch: {max_size: 8, max_wait: 5m}\n    checks: {retry_failed: 1}\n", "936a86848e84606ba89a7ae1ee56db20d204957d800189accb6a83ea9aa505e8"},
	} {
		cfg, _, err := config.Load([]byte(base + tc.extra))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Queues[0].Digest(true); got != tc.want {
			t.Fatalf("legacy digest %s != %s", got, tc.want)
		}
	}
}

func FuzzParsePolicyCandidate(f *testing.F) {
	for _, seed := range []string{"", "mode: ordered", "mode: ordered\nallow_from: [https://github.com/acme/api]", "mode: ordered\nmode: ordered", "mode: ordered\nallow_from: &x []\nallow_to: *x", "!!map {mode: ordered}"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		policy, err := ParsePolicyCandidate(data)
		if err != nil {
			return
		}
		if err := policy.Validate(); err != nil {
			t.Fatalf("accepted invalid policy: %v", err)
		}
		// Successful parsing is stable through canonical YAML serialization.
		raw, err := yaml.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		reparsed, err := ParsePolicyCandidate(raw)
		if err != nil || policy.Mode != reparsed.Mode || policy.SingleMaintainer != reparsed.SingleMaintainer || !slices.Equal(policy.AllowFrom, reparsed.AllowFrom) || !slices.Equal(policy.AllowTo, reparsed.AllowTo) {
			t.Fatalf("roundtrip: %+v %v", reparsed, err)
		}
	})
}
