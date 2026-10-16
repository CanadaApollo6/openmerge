package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSetupCLI(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	for _, tt := range []struct {
		args    []string
		code    int
		message string
	}{
		{[]string{"setup"}, 0, "openmerge setup github"},
		{[]string{"setup", "--help"}, 0, "GITHUB_TOKEN"},
		{[]string{"setup", "github", "--help"}, 0, "--webhook-url"},
		{[]string{"setup", "rulesets", "--help"}, 0, "--apply"},
		{[]string{"setup", "github", "--user", "--owner", "acme"}, 2, "mutually exclusive"},
		{[]string{"setup", "github", "extra"}, 2, "no positional arguments"},
		{[]string{"setup", "unknown"}, 2, "Usage:"},
		{[]string{"setup", "rulesets", "--repo", "acme/project", "--app-id", "42"}, 1, "GITHUB_TOKEN is required"},
		{[]string{"setup", "rulesets", "--repo", "invalid", "--app-id", "42"}, 1, "owner/name"},
		{[]string{"setup", "rulesets", "--repo", "acme/project"}, 1, "provide --app-id"},
		{[]string{"setup", "rulesets", "--app-id", "42", "--config-dir", "/unused"}, 2, "either --app-id"},
		{[]string{"setup", "github", "--webhook-url", "http://example.test"}, 1, "public HTTPS"},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(tt.args, &stdout, &stderr); got != tt.code || !strings.Contains(stdout.String()+stderr.String(), tt.message) {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", tt.args, got, stdout.String(), stderr.String())
		}
	}
}
