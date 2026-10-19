package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestPublicPolicySchemaNegativeFormats(t *testing.T) {
	for name, raw := range map[string]string{
		"member":            "groups: {g: ['@alice']}\n",
		"agent identity":    "agents: [{identity: a/b, accountable: [alice]}]\n",
		"accountable login": "agents: [{identity: helper, accountable: ['@alice']}]\n",
		"blank glob":        "classification: {docs: ['   ']}\n",
		"glob":              "classification: {docs: ['bad\\']}\n",
		"rule glob":         "queues: [{name: main, merge_when: [check-success = ci], approvals: {default: any_writer, rules: [{when: {touches: ['bad\\']}, require: any_writer}]}}]\n",
		"label":             "queues: [{name: main, merge_when: [check-success = ci], approvals: {default: any_writer, rules: [{when: {labels: [banana]}, require: any_writer}]}}]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(raw, "queues:") {
				raw += "queues: [{name: main, merge_when: [check-success = ci]}]\n"
			}
			data := []byte("version: 1\n" + raw)
			_, _, err := Load(data)
			if err == nil {
				t.Fatal("loader accepted invalid policy")
			}
			assertSchemaAgreement(t, data, err)
		})
	}
}

func TestPolicyDiagnosticsOriginalLocation(t *testing.T) {
	for _, tc := range []struct{ policy, want string }{
		{"    approvals:\n      default: {human: true, count: nope}\n", "line 8: queues[0].approvals.default.count: count must be a positive integer"},
		{"    approvals:\n      default: {human: true, count: 0}\n", "line 8: queues[0].approvals.default.count: count must be a positive integer"},
		{"    approvals:\n      default: any_writer\n      rules:\n      - when: {labels: [banana]}\n        require: any_writer\n", "line 10: queues[0].approvals.rules[0].when.labels[0]: invalid classification label banana"},
	} {
		data := "# original line one\nversion: 1\n\nqueues:\n  - name: main\n    merge_when: [check-success = ci]\n" + tc.policy
		_, _, err := Load([]byte(data))
		if err == nil || err.Error() != tc.want {
			t.Fatalf("got %v, want %s", err, tc.want)
		}
	}
}

func TestEmptyPolicyKeysPreserveLegacyConfig(t *testing.T) {
	const plain = "version: 1\nqueues: [{name: main, merge_when: [check-success = ci]}]\n"
	before, warnings, err := Load([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"agents: []\n", "groups: {}\n", "classification: {}\n", "agents: []\ngroups: {}\nclassification: {}\n"} {
		t.Run(extra, func(t *testing.T) {
			cfg, afterWarnings, err := Load([]byte(plain + extra))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Queues[0].Approvals != nil || cfg.Queues[0].Digest(true) != before.Queues[0].Digest(true) || !reflect.DeepEqual(warnings, afterWarnings) {
				t.Fatal("empty keys changed legacy queue or warnings")
			}
			assertSchemaAgreement(t, []byte(plain+extra), nil)
		})
	}
	withChanges := strings.Replace(plain, "name: main,", "name: main, changes: {},", 1)
	cfg, afterWarnings, err := Load([]byte(withChanges))
	if err != nil || cfg.Queues[0].Approvals != nil || cfg.Queues[0].Digest(true) != before.Queues[0].Digest(true) || !reflect.DeepEqual(warnings, afterWarnings) {
		t.Fatalf("empty changes altered legacy behavior: %v", err)
	}
	assertSchemaAgreement(t, []byte(withChanges), nil)
	_, _, err = Load([]byte(plain + "group: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "did you mean \"groups\"") {
		t.Fatalf("changed group diagnostic: %v", err)
	}
}

func TestPublicPolicySchemaValidFormats(t *testing.T) {
	for _, extra := range []string{
		"agents: [{identity: 'Helper[BOT]', accountable: [ALICE]}]\n",
		"groups: {g: [Alice, Org/Team_name, 'Helper[BOT]', 'Kate', 'ſam']}\n",
		"classification: {docs: ['docs/**', 'literal\\\\', 'escaped\\?']}\n",
	} {
		data := []byte("version: 1\n" + extra + "queues: [{name: main, merge_when: [check-success = ci]}]\n")
		_, _, err := Load(data)
		if err != nil {
			t.Fatal(err)
		}
		assertSchemaAgreement(t, data, nil)
	}
}
