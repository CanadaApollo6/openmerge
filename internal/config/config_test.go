package config

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestFind(t *testing.T) {
	paths := []string{".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml"}
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"none", nil, ErrNotFound.Error() + "; searched: " + strings.Join(paths, ", ")},
		{"root yml", paths[:1], ""}, {"root yaml", paths[1:2], ""},
		{"github yml", paths[2:3], ""}, {"github yaml", paths[3:], ""},
		{"two", []string{paths[0], paths[2]}, "multiple OpenMerge configuration files found: .openmerge.yml, .github/openmerge.yml"},
		{"all", paths, "multiple OpenMerge configuration files found: " + strings.Join(paths, ", ")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var reads []string
			path, data, err := Find(func(path string) ([]byte, bool, error) {
				reads = append(reads, path)
				for _, file := range tt.files {
					if path == file {
						return []byte(path), true, nil
					}
				}
				return nil, false, nil
			})
			if !reflect.DeepEqual(reads, paths) {
				t.Errorf("reads = %v, want %v", reads, paths)
			}
			if tt.want != "" {
				if err == nil || err.Error() != tt.want || path != "" || data != nil {
					t.Fatalf("Find = %q, %q, %v; want only error %q", path, data, err, tt.want)
				}
				if len(tt.files) == 0 && !errors.Is(err, ErrNotFound) {
					t.Errorf("error does not wrap ErrNotFound: %v", err)
				}
			} else if err != nil || path != tt.files[0] || string(data) != path {
				t.Fatalf("Find = %q, %q, %v", path, data, err)
			}
		})
	}
}

func TestFindReadError(t *testing.T) {
	want := errors.New("permission denied")
	_, _, err := Find(func(path string) ([]byte, bool, error) {
		if path == ".openmerge.yaml" {
			return nil, false, want
		}
		return []byte("version: 1"), true, nil
	})
	if !errors.Is(err, want) || err.Error() != "read .openmerge.yaml: permission denied" {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name, fields string
		method       queue.MergeMethod
		timeout      time.Duration
		batch        Batch
		warnings     int
	}{
		{"defaults", "", queue.Squash, time.Hour, Batch{MaxSize: 1}, 0},
		{"merge", "    merge_method: merge\n", queue.Merge, time.Hour, Batch{MaxSize: 1}, 0},
		{"rebase", "    merge_method: rebase\n", queue.Rebase, time.Hour, Batch{MaxSize: 1}, 0},
		{"squash", "    merge_method: squash\n", queue.Squash, time.Hour, Batch{MaxSize: 1}, 0},
		{"timeout", "    check_timeout: 1h2m3s\n", queue.Squash, time.Hour + 2*time.Minute + 3*time.Second, Batch{MaxSize: 1}, 0},
		{"disabled string", "    check_timeout: '0'\n", queue.Squash, 0, Batch{MaxSize: 1}, 0},
		{"disabled scalar", "    check_timeout: 0\n", queue.Squash, 0, Batch{MaxSize: 1}, 0},
		{"empty lists", "    enter_when: []\n    merge_when: []\n", queue.Squash, time.Hour, Batch{MaxSize: 1}, 0},
		{"empty batch", "    batch: {}\n", queue.Squash, time.Hour, Batch{MaxSize: 1}, 0},
		{"serial batch", "    batch: {max_size: 1, max_wait: 0s}\n", queue.Squash, time.Hour, Batch{MaxSize: 1}, 0},
		{"future batch", "    batch: {max_size: 128, max_wait: 30s}\n", queue.Squash, time.Hour, Batch{128, 30 * time.Second}, 1},
		{"inactive wait", "    batch: {max_wait: 5s}\n", queue.Squash, time.Hour, Batch{1, 5 * time.Second}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, warnings, err := Load([]byte("version: 1\nqueues:\n  - name: default\n" + tt.fields))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Version != 1 || len(cfg.Queues) != 1 || cfg.Queues[0].Name != "default" {
				t.Fatalf("config = %+v", cfg)
			}
			q := cfg.Queues[0]
			if q.MergeMethod != tt.method || q.CheckTimeout != tt.timeout || q.Batch != tt.batch || len(warnings) != tt.warnings {
				t.Errorf("queue = %+v, warnings = %v", q, warnings)
			}
			for _, expression := range []condition.Expression{q.EnterWhen, q.MergeWhen} {
				if ok, failed := expression.Evaluate(nil); !ok || failed != nil {
					t.Errorf("empty conditions = %v, %v", ok, failed)
				}
			}
			wantPlanner := queue.Config{
				MergeMethod: tt.method, CheckTimeout: tt.timeout,
				ResolveAfter: time.Minute, RefreshInterval: time.Minute, BuildRetries: 3, BuildBackoff: 5 * time.Second,
			}
			if got := q.QueueConfig(); got != wantPlanner {
				t.Errorf("QueueConfig = %+v, want %+v", got, wantPlanner)
			}
			if len(warnings) != 0 {
				want := Diagnostic{Line: 4, Path: "queues[0].batch", Message: "batching is not yet active; Phase 1 executes max_size 1 without waiting"}
				if warnings[0] != want {
					t.Errorf("warning = %v, want %v", warnings[0], want)
				}
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	const prefix = "version: 1\nqueues:\n  - name: default\n"
	tests := []struct{ name, input, want string }{
		{"empty", "", "line 1: version: 1 and exactly one queue are required"},
		{"missing version", "queues: [{name: default}]", "line 1: version: required; supported versions: 1"},
		{"old version", "version: 0\nqueues: [{name: default}]", `line 1: version: unsupported version "0"; supported versions: 1`},
		{"future version", "version: 2\nqueues: [{name: default}]", `line 1: version: unsupported version "2"; supported versions: 1`},
		{"string version", "version: '1'\nqueues: [{name: default}]", `line 1: version: unsupported version "1"; supported versions: 1`},
		{"float version", "version: 1.0\nqueues: [{name: default}]", `line 1: version: unsupported version "1.0"; supported versions: 1`},
		{"null version", "version:\nqueues: [{name: default}]", `line 1: version: unsupported version ""; supported versions: 1`},
		{"root shape", "[]", "line 1: expected a mapping"},
		{"missing queues", "version: 1", "line 1: queues: exactly one queue is required"},
		{"empty queues", "version: 1\nqueues: []", "line 2: queues: exactly one queue is required"},
		{"queues shape", "version: 1\nqueues: {}", "line 2: queues: expected a list containing exactly one queue"},
		{"multiple queues", "version: 1\nqueues: [{name: one}, {name: two}]", "line 2: queues: multiple queues arrive in a later release"},
		{"queue shape", "version: 1\nqueues: [oops]", "line 2: queues[0]: expected a mapping"},
		{"missing name", "version: 1\nqueues: [{}]", "line 2: queues[0].name: required"},
		{"empty name", "version: 1\nqueues: [{name: ' '}]", "line 2: queues[0].name: must not be empty"},
		{"typed name", "version: 1\nqueues: [{name: true}]", "line 2: queues[0].name: expected a string"},
		{"unknown root", prefix + "prioritis: []", `line 4: prioritis: unknown key "prioritis"`},
		{"unknown queue", prefix + "    merge_methd: squash", `line 4: queues[0].merge_methd: unknown key "merge_methd"; did you mean "merge_method"?`},
		{"unknown batch", prefix + "    batch:\n      max_siz: 2", `line 5: queues[0].batch.max_siz: unknown key "max_siz"; did you mean "max_size"?`},
		{"invalid method", prefix + "    merge_method: fast-forward", "line 4: queues[0].merge_method: expected merge, squash, or rebase"},
		{"typed method", prefix + "    merge_method: 1", "line 4: queues[0].merge_method: expected a string"},
		{"invalid duration", prefix + "    check_timeout: tomorrow", "line 4: queues[0].check_timeout: expected a nonnegative Go duration string"},
		{"negative duration", prefix + "    check_timeout: -1s", "line 4: queues[0].check_timeout: expected a nonnegative Go duration string"},
		{"duration type", prefix + "    check_timeout: 10", "line 4: queues[0].check_timeout: expected a nonnegative Go duration string"},
		{"duration overflow", prefix + "    check_timeout: 99999999999999999999h", "line 4: queues[0].check_timeout: expected a nonnegative Go duration string"},
		{"batch shape", prefix + "    batch: 2", "line 4: queues[0].batch: expected a mapping"},
		{"batch size zero", prefix + "    batch: {max_size: 0}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch size large", prefix + "    batch: {max_size: 129}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch size negative", prefix + "    batch: {max_size: -1}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch size type", prefix + "    batch: {max_size: '8'}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch size float", prefix + "    batch: {max_size: 1.5}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch size null", prefix + "    batch: {max_size: null}", "line 4: queues[0].batch.max_size: expected an integer from 1 to 128"},
		{"batch wait invalid", prefix + "    batch: {max_wait: forever}", "line 4: queues[0].batch.max_wait: expected a nonnegative Go duration string"},
		{"batch wait negative", prefix + "    batch: {max_wait: -5s}", "line 4: queues[0].batch.max_wait: expected a nonnegative Go duration string"},
		{"enter shape", prefix + "    enter_when: draft", "line 4: queues[0].enter_when: expected a list of conditions"},
		{"merge shape", prefix + "    merge_when: null", "line 4: queues[0].merge_when: expected a list of conditions"},
		{"condition type", prefix + "    enter_when: [true]", "line 4: queues[0].enter_when[0]: expected a condition string or an and/or/not mapping"},
		{"condition suggestion", prefix + "    enter_when:\n      - drfat", `line 5: queues[0].enter_when[0]: unknown attribute "drfat"; did you mean "draft"?`},
		{"operator suggestion", prefix + "    enter_when: [base glbo main]", `line 4: queues[0].enter_when[0]: unknown operator "glbo"; did you mean "glob"?`},
		{"invalid regex", prefix + "    merge_when: ['title ~= [']", `line 4: queues[0].merge_when[0]: invalid ~= pattern "["`},
		{"invalid boolean", prefix + "    enter_when: [draft = foo]", "line 4: queues[0].enter_when[0]: draft requires = or != and a boolean"},
		{"invalid list comparison", prefix + "    enter_when: [label > 3]", "line 4: queues[0].enter_when[0]: label requires a string operator"},
		{"invalid length", prefix + "    enter_when: ['#draft']", "line 4: queues[0].enter_when[0]: #draft requires a list attribute"},
		{"unknown nested key", prefix + "    enter_when: [{adn: [draft]}]", `line 4: queues[0].enter_when[0].adn: unknown key "adn"; did you mean "and"?`},
		{"AND shape", prefix + "    enter_when: [{and: draft}]", "line 4: queues[0].enter_when[0].and: expected a list of conditions"},
		{"OR shape", prefix + "    enter_when: [{or: {not: draft}}]", "line 4: queues[0].enter_when[0].or: expected a list of conditions"},
		{"NOT shape", prefix + "    enter_when: [{not: [draft, conflict]}]", "line 4: queues[0].enter_when[0].not: expected a condition string"},
		{"empty group", prefix + "    enter_when: [{}]", "line 4: queues[0].enter_when[0]: expected exactly one of and, or, not"},
		{"group siblings", prefix + "    enter_when: [{and: [], or: []}]", "line 4: queues[0].enter_when[0]: expected exactly one of and, or, not"},
		{"duplicate key", prefix + "    name: other", `line 4: duplicate key "name" (first defined on line 3)`},
		{"nonstring key", prefix + "    1: oops", "line 4: queues[0]: expected a string key"},
		{"syntax", "version: 1\nqueues: [\n", "line 2: yaml: line 2: did not find expected node content"},
		{"extra document", prefix + "---\nversion: 1\n", "expected exactly one YAML document"},
		{"undefined alias", prefix + "    enter_when: *missing", "unknown anchor 'missing' referenced"},
		{"cyclic alias", prefix + "    enter_when: &cycle [*cycle]", "line 4: cyclic alias or YAML expansion limit exceeded"},
		{"invalid merge", prefix + "    batch: {<<: 1}", "line 4: YAML merge requires a mapping or list of mappings"},
		{"duplicate merge", prefix + "    batch: {<<: {}, <<: {}}", `line 4: duplicate key "<<"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, warnings, err := Load([]byte(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if !reflect.DeepEqual(cfg, Config{}) || warnings != nil {
				t.Errorf("invalid input returned usable config or warnings: %+v, %v", cfg, warnings)
			}
			var diagnostics Errors
			if !errors.As(err, &diagnostics) || len(diagnostics) == 0 {
				t.Fatalf("error has no structured diagnostics: %v", err)
			}
			for _, diagnostic := range diagnostics {
				if diagnostic.Line < 1 {
					t.Errorf("error missing line: %+v", diagnostic)
				}
			}
		})
	}
}

func TestNestedConditionsAndAnchors(t *testing.T) {
	data := []byte(`version: 1
queues:
  - name: default
    enter_when: &conditions
      - not: draft
      - or:
          - and:
              - label = ready
              - "#approved-reviews-by >= 1"
          - author = maintainer
    merge_when: *conditions
    batch: &batch {max_size: 8, max_wait: 30s}
`)
	cfg, warnings, err := Load(data)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("Load: %v, warnings: %v", err, warnings)
	}
	for _, e := range []condition.Expression{cfg.Queues[0].EnterWhen, cfg.Queues[0].MergeWhen} {
		for _, tt := range []struct {
			pr   condition.Snapshot
			want bool
			fail []string
		}{
			{condition.Snapshot{"draft": condition.Bool(false), "label": condition.Strings("ready"), "approved-reviews-by": condition.Strings("alice")}, true, nil},
			{condition.Snapshot{"draft": condition.Bool(false), "author": condition.String("maintainer")}, true, nil},
			{condition.Snapshot{"draft": condition.Bool(true), "author": condition.String("maintainer")}, false, []string{"not (draft)"}},
			{condition.Snapshot{"draft": condition.Bool(false)}, false, []string{`or (and (label = "ready"; #approved-reviews-by >= 1); author = "maintainer")`}},
		} {
			got, failures := e.Evaluate(tt.pr)
			var conditions []string
			for _, failure := range failures {
				conditions = append(conditions, failure.String())
			}
			if got != tt.want || !reflect.DeepEqual(conditions, tt.fail) {
				t.Errorf("Evaluate = %v, %v; want %v, %v", got, failures, tt.want, tt.fail)
			}
		}
	}
}

func TestAliasReuseAndMergeKeys(t *testing.T) {
	tests := []struct{ name, fields string }{
		{"scalar", "    merge_method: &method squash\n    enter_when: [&condition 'label = ready']\n    merge_when: [*condition]\n"},
		{"queue", "    <<: &queue {merge_method: squash, enter_when: ['label = ready']}\n    merge_when: ['label = ready']\n"},
		{"batch mapping", "    batch: {<<: &batch {max_size: 8, max_wait: 30s}, max_size: 1, max_wait: 0s}\n"},
		{"batch sequence", "    batch: {<<: [{max_size: 1}, {max_size: 8, max_wait: 0s}]}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, warnings, err := Load([]byte("version: 1\nqueues:\n  - name: default\n" + tt.fields))
			if err != nil || len(warnings) != 0 {
				t.Fatalf("Load = %v, warnings = %v", err, warnings)
			}
			q := cfg.Queues[0]
			if q.Batch.MaxSize != 1 || q.Batch.MaxWait != 0 || q.MergeMethod != queue.Squash {
				t.Fatalf("queue = %+v", q)
			}
			if ok, _ := q.EnterWhen.Evaluate(condition.Snapshot{"label": condition.Strings("ready")}); !ok {
				t.Fatal("anchor conditions did not evaluate")
			}
		})
	}
}

func TestGoldenExampleMatchesADR(t *testing.T) {
	data, err := os.ReadFile("testdata/openmerge.yml")
	if err != nil {
		t.Fatal(err)
	}
	adr, err := os.ReadFile("../../docs/adr/0003-configuration-format.md")
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(adr), "```yaml\n")
	if !found {
		t.Fatal("ADR YAML example not found")
	}
	example, _, found := strings.Cut(block, "```\n")
	if !found || string(data) != example {
		t.Fatal("golden example differs from ADR 0003")
	}
	cfg, warnings, err := Load(data)
	if err != nil {
		t.Fatalf("ADR example does not validate: %s", err)
	}
	if len(warnings) != 1 || cfg.Queues[0].Batch != (Batch{8, 30 * time.Second}) {
		t.Fatalf("example = %+v, warnings = %v", cfg, warnings)
	}
	pr := condition.Snapshot{"approved-reviews-by": condition.Strings("alice"), "check-success": condition.Strings("lint", "integration")}
	for _, e := range []condition.Expression{cfg.Queues[0].EnterWhen, cfg.Queues[0].MergeWhen} {
		if ok, failed := e.Evaluate(pr); !ok {
			t.Errorf("example conditions failed: %v", failed)
		}
	}
}
