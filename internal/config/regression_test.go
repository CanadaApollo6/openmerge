package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

func TestNegatedConditionsFailClosed(t *testing.T) {
	for _, text := range []string{"conflict", "#approved-reviews-by < 1"} {
		data := fmt.Sprintf("version: 1\nqueues:\n  - name: default\n    enter_when: &rules\n      - not: %q\n    merge_when: *rules\n", text)
		cfg, _, err := Load([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, expression := range []condition.Expression{cfg.Queues[0].EnterWhen, cfg.Queues[0].MergeWhen} {
			for _, pr := range []condition.PR{nil, condition.Snapshot{
				"conflict": condition.String("false"), "approved-reviews-by": condition.Bool(false),
			}} {
				ok, failures := expression.Evaluate(pr)
				if ok || len(failures) != 1 || !failures[0].Unknown || len(failures[0].Children) != 1 || failures[0].Children[0].Attribute == "" {
					t.Errorf("%s admitted unknown data: %v, %+v", text, ok, failures)
				}
			}
		}
	}
}

func TestDuplicateKeysBeforeMerge(t *testing.T) {
	const prefix = "version: 1\nqueues:\n  - name: default\n"
	tests := []struct {
		name, input, key string
		line, first      int
	}{
		{"root", "version: 1\nversion: 1\nqueues: [{name: default}]", "version", 2, 1},
		{"queue", prefix + "    name: other", "name", 4, 3},
		{"merge source", prefix + "    <<:\n      merge_method: squash\n      merge_method: rebase\n    merge_method: merge", "merge_method", 6, 5},
		{"merge sequence source", prefix + "    batch:\n      <<:\n        - max_size: 1\n        - max_size: 8\n          max_size: 16", "max_size", 8, 7},
		{"condition source", prefix + "    enter_when:\n      - <<:\n          not: draft\n          not: conflict", "not", 7, 6},
		{"discarded source value", prefix + "    batch:\n      <<:\n        max_size: {unused: {nested: 1, nested: 2}}\n      max_size: 1", "nested", 6, 6},
		{"merge key", prefix + "    batch:\n      <<: {}\n      <<: {}", "<<", 6, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Load([]byte(tt.input))
			want := fmt.Sprintf("line %d: duplicate key %q (first defined on line %d)", tt.line, tt.key, tt.first)
			if err == nil || err.Error() != want {
				t.Fatalf("Load error = %v, want %s", err, want)
			}
		})
	}
}

func TestMergeKeyIdentity(t *testing.T) {
	const prefix = "version: 1\nqueues:\n  - name: default\n"
	for _, key := range []string{"!!merge not", "!!merge ''", "!!merge [not]"} {
		_, _, err := Load([]byte(prefix + "    enter_when:\n      - " + key + ": {not: draft}\n"))
		if err == nil || !strings.Contains(err.Error(), "line 5: YAML merge key must be a scalar with value <<") {
			t.Errorf("key %s: %v", key, err)
		}
	}
	// Value, rather than YAML's inferred tag, identifies an actual merge key.
	for _, key := range []string{"<<", "'<<'", "!!str <<", "!!merge <<"} {
		cfg, _, err := Load([]byte(prefix + "    batch: {" + key + ": {max_size: 8}, max_size: 1}\n"))
		if err != nil || cfg.Queues[0].Batch.MaxSize != 1 {
			t.Errorf("merge key %s: %+v, %v", key, cfg, err)
		}
	}
}

func TestUnquotedLengthConditionHint(t *testing.T) {
	_, _, err := Load([]byte("version: 1\nqueues:\n  - name: default\n    enter_when:\n      - #approved-reviews-by >= 1\n"))
	if err == nil || !strings.Contains(err.Error(), "line 5:") || !strings.Contains(err.Error(), "conditions starting with # must be quoted") {
		t.Fatalf("missing YAML comment hint: %v", err)
	}
}

func TestUnknownKeySuggestions(t *testing.T) {
	for _, input := range []string{
		"version: 1\nqueues: [{name: default}]\npriorities: []\n",
		"version: 1\nqueues: [{name: default, Totally_Unknown: []}]\n",
	} {
		_, _, err := Load([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "unknown key") || strings.Contains(err.Error(), "did you mean") {
			t.Errorf("misleading schema suggestion: %v", err)
		}
	}
}

func TestDiagnosticsSortedByLine(t *testing.T) {
	_, _, err := Load([]byte(`version: 1
queues:
  - enter_when: [drfat]
    check_timeout: tomorrow
    merge_method: invalid
    merge_methd: squash
`))
	var diagnostics Errors
	if !errors.As(err, &diagnostics) {
		t.Fatalf("missing diagnostics: %v", err)
	}
	var lines []int
	for _, diagnostic := range diagnostics {
		lines = append(lines, diagnostic.Line)
	}
	if want := []int{3, 3, 4, 5, 6}; !reflect.DeepEqual(lines, want) {
		t.Errorf("diagnostic lines = %v, want %v", lines, want)
	}
}

func TestYAMLResourceLimits(t *testing.T) {
	const prefix = "version: 1\nqueues:\n  - name: default\n    enter_when:\n"
	aliasInput := func(levels int) string {
		var input strings.Builder
		input.WriteString(prefix)
		input.WriteString("      - &a0 {and: [draft, draft]}\n")
		for i := 1; i <= levels; i++ {
			fmt.Fprintf(&input, "      - &a%d {and: [*a%d, *a%d]}\n", i, i-1, i-1)
		}
		return input.String()
	}
	nestedInput := func(levels int) string {
		return prefix + "      - " + strings.Repeat("{not: ", levels) + "draft" + strings.Repeat("}", levels) + "\n"
	}
	for _, test := range []struct {
		name, data string
		valid      bool
	}{
		{"bounded aliases", aliasInput(4), true},
		{"alias expansion", aliasInput(13), false},
		{"bounded nesting", nestedInput(20), true},
		{"nesting depth", nestedInput(120), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, warnings, err := Load([]byte(test.data))
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				if ok, failures := cfg.Queues[0].EnterWhen.Evaluate(condition.Snapshot{"draft": condition.Bool(true)}); !ok {
					t.Fatalf("bounded input failed: %+v", failures)
				}
				return
			}
			var diagnostics Errors
			if !errors.As(err, &diagnostics) || len(diagnostics) != 1 || diagnostics[0].Line < 5 || !strings.Contains(err.Error(), "YAML expansion limit exceeded") {
				t.Fatalf("expected located resource limit, got %v", err)
			}
			if !reflect.DeepEqual(cfg, Config{}) || warnings != nil {
				t.Fatal("resource limit returned usable configuration")
			}
		})
	}
}

func TestREADMEConditionContract(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"The ADR is unchanged", "Its comment also lists"} {
		if strings.Contains(string(readme), stale) {
			t.Errorf("README still claims %q", stale)
		}
	}
	adr, err := os.ReadFile("../../docs/adr/0003-configuration-format.md")
	if err != nil {
		t.Fatal(err)
	}
	const rule = "Missing or mistyped attribute data makes a condition unknown, and unknown never passes, including under `not`."
	if !strings.Contains(string(adr), rule) {
		t.Error("ADR does not document fail-closed negation")
	}
}
