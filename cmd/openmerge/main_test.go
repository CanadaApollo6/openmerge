package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestRun(t *testing.T) {
	const wantUsage = "Usage: openmerge <command>\n\nCommands:\n  version                 Print version and build information\n  config validate [path]  Validate repository configuration\n  help                    Show this help\n"
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
		files      map[string]string
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantStdout: `openmerge \S+\ncommit: (unknown|[[:xdigit:]]{40})\ngo: ` + regexp.QuoteMeta(runtime.Version()) + `\n`,
		},
		{
			name:       "help",
			args:       []string{"help"},
			wantStdout: regexp.QuoteMeta(wantUsage),
		},
		{
			name:       "no arguments",
			wantStdout: regexp.QuoteMeta(wantUsage),
		},
		{
			name:       "unknown command",
			args:       []string{"queue"},
			wantCode:   2,
			wantStderr: "Unknown command \"queue\"\n\n" + wantUsage,
		},
		{
			name:       "extra version argument",
			args:       []string{"version", "extra"},
			wantCode:   2,
			wantStderr: "Unknown command \"version extra\"\n\n" + wantUsage,
		},
		{
			name:       "extra help argument",
			args:       []string{"help", "extra"},
			wantCode:   2,
			wantStderr: "Unknown command \"help extra\"\n\n" + wantUsage,
		},
		{
			name: "validate discovered config", args: []string{"config", "validate"},
			files:      map[string]string{".github/openmerge.yaml": "version: 1\nqueues: [{name: default}]\n"},
			wantStdout: "ok\n",
		},
		{
			name: "validate explicit config", args: []string{"config", "validate", "custom.yml"},
			files:      map[string]string{"custom.yml": "version: 1\nqueues: [{name: default}]\n", ".openmerge.yml": "invalid"},
			wantStdout: "ok\n",
		},
		{
			name: "validate help", args: []string{"config", "validate", "--help"},
			wantStdout: "Usage: openmerge config validate \\[path\\]\n",
		},
		{
			name: "validate short help", args: []string{"config", "validate", "-h"},
			wantStdout: "Usage: openmerge config validate \\[path\\]\n",
		},
		{
			name: "validate missing explicit path", args: []string{"config", "validate", "missing.yml"}, wantCode: 1,
			wantStderr: "open missing.yml: no such file or directory\n",
		},
		{
			name: "validate warnings", args: []string{"config", "validate"},
			files:      map[string]string{".openmerge.yml": "version: 1\nqueues:\n  - name: default\n    batch: {max_size: 8, max_wait: 30s}\n"},
			wantStdout: "ok\n",
			wantStderr: ".openmerge.yml:4: warning: queues[0].batch: batching is not yet active; Phase 1 executes max_size 1 without waiting\n",
		},
		{
			name: "validate all errors", args: []string{"config", "validate"}, wantCode: 1,
			files: map[string]string{".openmerge.yml": "version: 2\nqueues:\n  - name: default\n    merge_methd: squash\n    enter_when: [drfat]\n"},
			wantStderr: ".openmerge.yml:1: version: unsupported version \"2\"; supported versions: 1\n" +
				".openmerge.yml:4: queues[0].merge_methd: unknown key \"merge_methd\"; did you mean \"merge_method\"?\n" +
				".openmerge.yml:5: queues[0].enter_when[0]: unknown attribute \"drfat\"; did you mean \"draft\"?\n",
		},
		{
			name: "validate missing config", args: []string{"config", "validate"}, wantCode: 1,
			wantStderr: "no OpenMerge configuration found; searched: .openmerge.yml, .openmerge.yaml, .github/openmerge.yml, .github/openmerge.yaml\n",
		},
		{
			name: "validate ambiguous lookup", args: []string{"config", "validate"}, wantCode: 1,
			files:      map[string]string{".openmerge.yml": "", ".github/openmerge.yml": ""},
			wantStderr: "multiple OpenMerge configuration files found: .openmerge.yml, .github/openmerge.yml\n",
		},
		{
			name: "validate ambiguity without root yml", args: []string{"config", "validate"}, wantCode: 1,
			files:      map[string]string{".github/openmerge.yml": "", ".github/openmerge.yaml": ""},
			wantStderr: "multiple OpenMerge configuration files found: .github/openmerge.yml, .github/openmerge.yaml\n",
		},
		{
			name: "validate all ambiguous files", args: []string{"config", "validate"}, wantCode: 1,
			files:      map[string]string{".openmerge.yml": "", ".openmerge.yaml": "", ".github/openmerge.yml": "", ".github/openmerge.yaml": ""},
			wantStderr: "multiple OpenMerge configuration files found: .openmerge.yml, .openmerge.yaml, .github/openmerge.yml, .github/openmerge.yaml\n",
		},
		{
			name: "validate YAML syntax error", args: []string{"config", "validate"}, wantCode: 1,
			files:      map[string]string{".openmerge.yml": "version: 1\nqueues: [\n"},
			wantStderr: ".openmerge.yml:2: yaml: line 2: did not find expected node content\n",
		},
		{
			name: "extra validation argument", args: []string{"config", "validate", "one.yml", "two.yml"}, wantCode: 2,
			wantStderr: "Unknown command \"config validate one.yml two.yml\"\n\n" + wantUsage,
		},
		{
			name: "unknown config subcommand", args: []string{"config", "import"}, wantCode: 2,
			wantStderr: "Unknown command \"config import\"\n\n" + wantUsage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for path, data := range tt.files {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !regexp.MustCompile(`\A` + tt.wantStdout + `\z`).MatchString(stdout.String()) {
				t.Errorf("stdout = %q, want pattern %q", stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestValidationOutputFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".openmerge.yml", []byte("version: 1\nqueues: [{name: default}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if got := run([]string{"config", "validate"}, brokenWriter{}, &stderr); got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
}
