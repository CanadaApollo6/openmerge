package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSimFlagErrorsAndHelp(t *testing.T) {
	for _, scenario := range []struct {
		args []string
		code int
		want string
	}{
		{args: []string{"sim", "--help"}, want: "Usage: openmerge sim"},
		{args: []string{"sim", "--unknown"}, code: 2, want: "flag provided but not defined: -unknown"},
		{args: []string{"sim", "--ci", "fast"}, code: 2, want: "invalid value"},
		{args: []string{"sim", "--seed", "-1"}, code: 2, want: "invalid value"},
	} {
		t.Run(strings.Join(scenario.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(scenario.args, &stdout, &stderr); code != scenario.code || !strings.Contains(stderr.String(), scenario.want) || stdout.Len() != 0 {
				t.Fatalf("code=%d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestSimOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"sim", "--prs", "0"}, {"sim", "--prs", "0", "--json"}} {
		var stderr bytes.Buffer
		if code := run(args, brokenWriter{}, &stderr); code != 1 {
			t.Fatalf("%v exit=%d, want 1", args, code)
		}
	}
}
