package config

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/schema"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestHistoricalStageAAuthoritySchemaComposition(t *testing.T) {
	var public, candidate map[string]any
	if err := json.Unmarshal([]byte(schema.V1), &public); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/authority.candidate.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &candidate); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the Stage A public surface, then compose its historical
	// candidate extension. Both fixtures remain immutable historical evidence.
	props := public["properties"].(map[string]any)
	props["policy_authority"] = map[string]any{"type": "string", "enum": []any{"github"}}
	delete(props, "break_glass")
	delete(public, "allOf")
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("historical-public.json", public); err != nil {
		t.Fatal(err)
	}
	historical, err := compiler.Compile("historical-public.json")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := os.ReadFile("testdata/authority-public.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Suffix string
		Valid        bool
	}
	if err := json.Unmarshal(corpus, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			data := []byte("version: 1\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n" + tc.Suffix)
			value, err := schemaInput(data)
			if err == nil {
				err = historical.Validate(value)
			}
			if (err == nil) != tc.Valid {
				t.Fatalf("historical public schema=%v", err)
			}
		})
	}
	for key, value := range candidate["properties"].(map[string]any) {
		props[key] = value
	}
	public["allOf"] = candidate["allOf"]
	compiler = jsonschema.NewCompiler()
	if err := compiler.AddResource("historical-candidate.json", public); err != nil {
		t.Fatal(err)
	}
	composed, err := compiler.Compile("historical-candidate.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"team: acme/emergency", "repository_admin: true"} {
		value, err := schemaInput([]byte("version: 1\nqueues: [{name: main, merge_when: ['check-success = ci']}]\npolicy_authority: openmerge\nbreak_glass: {" + actor + "}\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := composed.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
}
