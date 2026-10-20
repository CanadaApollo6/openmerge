package github

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// These REST payloads pass through the same decoder/normalizer as the PR 2
// reader. No hand-normalized rule names or array paths enter the evaluator.
func TestTransportRulesReachPushEvaluator(t *testing.T) {
	for _, tc := range []struct {
		name, rule                   string
		create, bypass, reject, warn bool
	}{
		{name: "size 1", rule: `{"type":"max_file_size","parameters":{"max_file_size":1}}`},
		{name: "size 100", rule: `{"type":"max_file_size","parameters":{"max_file_size":100}}`},
		{name: "size 0", rule: `{"type":"max_file_size","parameters":{"max_file_size":0}}`, reject: true},
		{name: "size 101", rule: `{"type":"max_file_size","parameters":{"max_file_size":101}}`, reject: true},
		{name: "old size type", rule: `{"type":"file_size_restriction","parameters":{"max_file_size":1}}`, reject: true},
		{name: "workflow bypass", rule: `{"type":"workflows","parameters":{"workflows":[{"repository_id":1,"path":".github/workflows/ci.yml"}]}}`, bypass: true, warn: true},
		{name: "workflow creation", rule: `{"type":"workflows","parameters":{"do_not_enforce_on_create":true,"workflows":[]}}`, create: true},
		{name: "checks creation", rule: `{"type":"required_status_checks","parameters":{"do_not_enforce_on_create":true,"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"ci","integration_id":42}]}}`, create: true},
		{name: "checks update", rule: `{"type":"required_status_checks","parameters":{"do_not_enforce_on_create":true,"required_status_checks":[]}}`, reject: true},
		{name: "unknown despite bypass", rule: `{"type":"required_status_checks","parameters":{"future_flag":true}}`, bypass: true, reject: true},
		{name: "unknown despite creation skip", rule: `{"type":"workflows","parameters":{"do_not_enforce_on_create":true,"future_flag":true}}`, create: true, reject: true},
		{name: "unsupported PR config", rule: `{"type":"pull_request","parameters":{"do_not_enforce_on_create":true}}`, create: true, bypass: true, reject: true},
		{name: "reviews zero", rule: `{"type":"pull_request","parameters":{"required_approving_review_count":0}}`, bypass: true, warn: true},
		{name: "path literal sentinel", rule: `{"type":"file_path_restriction","parameters":{"restricted_file_paths":["~ALL"]}}`},
		{name: "extension", rule: `{"type":"file_extension_restriction","parameters":{"restricted_file_extensions":[".pem"]}}`},
		{name: "path length", rule: `{"type":"max_file_path_length","parameters":{"max_file_path_length":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire authorityRuleset
			raw := `{"id":1,"source_type":"Repository","source":"acme/project","target":"push","enforcement":"active","conditions":{},"rules":[` + tc.rule + `],"bypass_actors":[]}`
			if err := json.Unmarshal([]byte(raw), &wire); err != nil {
				t.Fatal(err)
			}
			policy, err := wire.protection("acme/project")
			if err != nil {
				t.Fatal(err)
			}
			if tc.bypass {
				policy.BypassActors = []authority.BypassActor{{Kind: "app", ID: "42", Mode: "always"}}
			}
			old, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
			ref := forge.PushRef{Ref: "refs/heads/topic", OldSHA: old, NewSHA: tip, Boundary: []string{old}, Commits: []forge.PushCommit{{OID: tip, Parents: []string{old}, Files: []forge.PushFile{}, Diffs: []forge.PushDiff{{Parent: old, Paths: []string{}}}}}}
			if tc.create {
				ref.OldSHA = ""
				ref.CreationBaseSHA = old
			}
			ctx := forge.PushRuleContext{AppID: "42", DefaultBranch: "main", Observation: authority.Observation{Inventory: authority.Visible, Findings: []authority.Finding{}, Coverage: []authority.Coverage{{Ref: "refs/heads/main", Visibility: authority.Visible, ProtectionIDs: []string{"repository_ruleset/1"}}}, Protections: []authority.Protection{policy}}}
			warnings, err := forge.EvaluatePushRules(forge.PreparedPush{Operation: forge.PushBuild, Complete: true, Refs: []forge.PushRef{ref}}, ctx)
			if (err != nil) != tc.reject || (len(warnings) > 0) != tc.warn {
				t.Fatalf("warnings=%v error=%v", warnings, err)
			}
		})
	}
}
