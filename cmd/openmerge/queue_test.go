package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const statusJSON = `{"version":1,"queue":{"installation":"7","repo":"owner/repo","base":"release/v1","lane":"","watermark":3,"base_sha":"base","paused":true,"pause_reason":"repository-rules","pause_detail":"fix fence","active_batch":null,"entries":[{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""}]},"pr":{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""},"why":{"summary":"PR #1: paused (repository-rules): fix fence","limit":20,"events":[]}}
`

func TestQueueStatusJSONIsVerbatimAndHumanIsCompact(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "test-secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/queues/owner/repo/release%2Fv1/prs/1" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Method != "GET" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, statusJSON)
	}))
	defer server.Close()
	args := []string{"queue", "status", "--api", server.URL, "--repo", "owner/repo", "--base", "release/v1", "--pr", "1"}
	var out, errOut bytes.Buffer
	if code := run(append(args, "--json"), &out, &errOut); code != 0 || out.String() != statusJSON || errOut.Len() > 0 {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
	out.Reset()
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, &errOut)
	}
	for _, text := range []string{"REPO", "owner/repo", "release/v1", "#1", "012345678901", "paused", "repository-rules", "fix fence"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("human output missing %q: %s", text, &out)
		}
	}
}

func TestQueueStatusExitsAndNeverFollowsRedirects(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{200, `{"version":1,"queues":[]}`, 0},
		{200, `{"version":99,"queues":[]}`, 1},
		{200, `{"version":1}`, 1},
		{200, `not JSON`, 1},
		{404, ``, 3}, {401, ``, 4}, {403, ``, 4}, {503, ``, 1}, {302, ``, 1},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			if code := runQueue([]string{"status", "--api", server.URL, "--json"}, &out, &errOut); code != tc.want || calls != 1 {
				t.Fatalf("code=%d want=%d calls=%d err=%s", code, tc.want, calls, &errOut)
			}
			if tc.want != 0 && out.Len() != 0 {
				t.Fatal("error emitted success JSON")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	if code := runQueue([]string{"status", "--api", server.URL}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("transport failure=%d", code)
	}
}

func TestQueueStatusUsageAndOutputFailures(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, args := range [][]string{nil, {"write"}, {"status", "extra"}, {"status", "--pr", "1"}, {"status", "--base", "main"}, {"status", "--repo", "bad"}, {"status", "--repo", "owner/repo", "--pr", "0"}, {"status", "--api", "file:///tmp/status"}, {"status", "--api", "http://user:secret@localhost"}, {"status", "--api", "http://localhost/path"}, {"status", "--database-url", "postgres://local"}} {
		if code := runQueue(args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	if code := runQueue([]string{"status", "--help"}, io.Discard, io.Discard); code != 0 {
		t.Fatal("help failed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, statusJSON) }))
	defer server.Close()
	for _, extra := range [][]string{nil, {"--json"}} {
		if code := runQueue(append([]string{"status", "--api", server.URL, "--repo", "owner/repo", "--pr", "1"}, extra...), brokenWriter{}, io.Discard); code != 1 {
			t.Fatalf("output failure=%d", code)
		}
	}
}
