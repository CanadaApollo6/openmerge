package main

import (
	"bytes"
	"regexp"
	"runtime"
	"testing"
)

func TestRun(t *testing.T) {
	const wantUsage = "Usage: openmerge <command>\n\nCommands:\n  version  Print version and build information\n  help     Show this help\n"
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
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
