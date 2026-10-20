package github

import (
	"encoding/json"
	"strings"
	"testing"
)

func classicReadFixture() map[string]any {
	fields := completeClassicFixture()
	fields["required_status_checks"] = map[string]any{"strict": false, "contexts": []any{}, "checks": []any{}}
	fields["required_pull_request_reviews"] = map[string]any{"bypass_pull_request_allowances": map[string]any{"apps": []any{map[string]any{"id": 42}}}}
	fields["restrictions"] = map[string]any{"apps": []any{map[string]any{"id": 42}}}
	fields["required_signatures"] = map[string]any{"enabled": false}
	return fields
}

func TestRollbackClassicRequiredFieldMatrix(t *testing.T) {
	paths := []string{
		"required_status_checks", "required_status_checks.strict", "required_status_checks.contexts", "required_status_checks.checks",
		"required_pull_request_reviews", "required_pull_request_reviews.bypass_pull_request_allowances", "required_pull_request_reviews.bypass_pull_request_allowances.apps", "required_pull_request_reviews.bypass_pull_request_allowances.apps.0.id",
		"restrictions", "restrictions.apps", "restrictions.apps.0.id",
		"required_linear_history", "required_linear_history.enabled", "allow_force_pushes", "allow_force_pushes.enabled", "lock_branch", "lock_branch.enabled",
		"required_signatures.enabled",
	}
	for _, path := range paths {
		for _, kind := range []string{"missing", "null", "wrong type"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				fields := classicReadFixture()
				parts := strings.Split(path, ".")
				var parent any = fields
				for _, part := range parts[:len(parts)-1] {
					if part == "0" {
						parent = parent.([]any)[0]
					} else {
						parent = parent.(map[string]any)[part]
					}
				}
				object := parent.(map[string]any)
				key := parts[len(parts)-1]
				switch kind {
				case "missing":
					delete(object, key)
				case "null":
					object[key] = nil
				case "wrong type":
					object[key] = "unknown"
				}
				// Explicit null is the API's absence representation for these
				// three kinds; it differs from a missing field or partial object.
				compatible := kind == "null" && (path == "required_status_checks" || path == "required_pull_request_reviews" || path == "restrictions")
				f := rulesAPI(t)
				opts := rollbackOptions(t, f)
				f.override = rollbackReads(t, f, fields, map[string]any{"enabled": false})
				out, err := runAuthority(t, f, opts)
				if compatible {
					must(t, err)
				} else if err == nil || !strings.Contains(out, "incomplete classic protection response") || strings.Contains(out, "Rollback preflight passed") {
					t.Fatalf("partial response certified: %s %v", out, err)
				}
				if f.setupWrites != 2 {
					t.Fatal("preflight mutated policies")
				}
			})
		}
	}
}

func TestClassicReadNestedUnknownValues(t *testing.T) {
	for _, field := range []string{"contexts", "checks", "review apps", "restriction apps"} {
		for _, invalid := range []any{nil, true, 42, map[string]any{}} {
			fields := classicReadFixture()
			switch field {
			case "contexts", "checks":
				fields["required_status_checks"].(map[string]any)[field] = []any{invalid}
			case "review apps":
				fields["required_pull_request_reviews"].(map[string]any)["bypass_pull_request_allowances"].(map[string]any)["apps"] = []any{invalid}
			case "restriction apps":
				fields["restrictions"].(map[string]any)["apps"] = []any{invalid}
			}
			raw, err := json.Marshal(fields)
			must(t, err)
			if err := completeClassicProtection(raw); err == nil {
				t.Fatalf("accepted incomplete %s: %s", field, raw)
			}
		}
	}
}
