package main

import (
	"bytes"
	"encoding/json"
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

func TestSimWorkloadFlagsAndJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"sim", "--prs", "6", "--broken-rate", "0", "--seed", "42", "--ci", "1s", "--ci-max", "2s", "--window", "0s", "--drop-webhooks", "1", "--merge-method", "rebase", "--repush-rate", "1", "--conflict-rate", "1", "--external-push-rate", "1", "--json"}
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var got struct {
		Config struct {
			Seed     uint64  `json:"seed"`
			CIMin    float64 `json:"ci_min_seconds"`
			CIMax    float64 `json:"ci_max_seconds"`
			Drop     float64 `json:"drop_webhook_rate"`
			Method   string  `json:"merge_method"`
			Window   float64 `json:"arrival_window_seconds"`
			Repush   float64 `json:"repush_rate"`
			Conflict float64 `json:"conflict_rate"`
			External float64 `json:"external_push_rate"`
		} `json:"config"`
		Dequeued       map[string]int `json:"dequeued"`
		Merged         int            `json:"merged"`
		Median         float64        `json:"median_time_to_merge_seconds"`
		FirstMedian    float64        `json:"median_time_from_first_enqueue_seconds"`
		ExternalPushes int            `json:"external_pushes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Config.Seed != 42 || got.Config.CIMin != 1 || got.Config.CIMax != 2 || got.Config.Drop != 1 || got.Config.Method != "rebase" || got.Config.Window != 0 || got.Config.Repush != 1 || got.Config.Conflict != 1 || got.Config.External != 1 {
		t.Fatalf("flags not reflected in effective config: %+v", got.Config)
	}
	if got.Dequeued["head-changed"] != 6 || got.Dequeued["conflict"] != 3 || got.Merged != 3 || got.Median <= 0 || got.FirstMedian <= got.Median || got.ExternalPushes == 0 {
		t.Fatalf("flags not reflected in workload: %+v", got)
	}
}

func TestSimInvalidWorkloadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--ci", "2s", "--ci-max", "1s"}, {"--ci-max", "-1s"},
		{"--drop-webhooks", "2"}, {"--merge-method", "invalid"}, {"--window", "-1s"},
		{"--repush-rate", "-1"}, {"--conflict-rate", "NaN"},
		{"--external-push-rate", "-1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"sim", "--prs", "0"}, args...), &stdout, &stderr); code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("accepted %v: code=%d out=%s err=%s", args, code, stdout.String(), stderr.String())
		}
	}
}
