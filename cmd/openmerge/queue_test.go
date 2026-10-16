package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/status"
)

const statusJSON = `{"version":1,"queue":{"installation":"7","repo":"owner/repo","base":"release/v1","lane":"","watermark":3,"base_sha":"base","paused":true,"pause_reason":"repository-rules","pause_detail":"fix fence","blocks":[{"reason":"repository-rules","detail":"fix fence"},{"reason":"action-poisoned","detail":"invalid action"}],"active_batch":null,"entries":[{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""}]},"pr":{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""},"why":{"summary":"PR #1: paused (repository-rules): fix fence","limit":20,"events":[{"id":3,"type":"rules_blocked","at":"2026-10-16T12:00:00Z","summary":"Repository rules block: fix fence"}]}}
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
	for _, text := range []string{"REPO", "owner/repo", "release/v1", "#1", "012345678901", "paused", "repository-rules", "action-poisoned", "rules_blocked", "Repository rules block: fix fence"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("human output missing %q: %s", text, &out)
		}
	}
}

func TestQueueStatusPrintsBoundedSanitizedServerErrors(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "local-api-secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		status int
		code   string
		want   int
	}{
		{400, "invalid-request", 2},
		{401, "unauthorized", 4},
		{404, "not-found", 3},
		{409, "ambiguous-queue", 5},
		{413, "request-too-large", 2},
		{503, "unavailable", 1},
		{409, "unknown-error", 1},
	} {
		t.Run(tc.code, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"version": 1, "error": tc.code, "message": "queue diagnostic: local-api-secret ghp_hidden123 Bearer hidden-token\n\x1b\u202e " + strings.Repeat("x", 2000)})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			code := runQueue([]string{"status", "--api", server.URL, "--json"}, &out, &errOut)
			if code != tc.want || out.Len() != 0 || !strings.Contains(errOut.String(), tc.code+": queue diagnostic:") || !strings.Contains(errOut.String(), "[REDACTED]") || errOut.Len() > 1400 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &errOut)
			}
			for _, unsafe := range []string{"local-api-secret", "hidden123", "hidden-token", "\x1b", "\u202e"} {
				if strings.Contains(errOut.String(), unsafe) {
					t.Fatalf("unsafe server diagnostic %q", unsafe)
				}
			}
		})
	}
	for _, body := range []string{`<html>private raw body</html>`, `{"version":1,"error":"unavailable","message":"` + strings.Repeat("x", 8192) + `"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, body)
		}))
		var out, errOut bytes.Buffer
		code := runQueue([]string{"status", "--api", server.URL}, &out, &errOut)
		server.Close()
		if code != 1 || out.Len() != 0 || errOut.String() != "status API returned HTTP 503\n" {
			t.Fatalf("invalid/oversized error: %d %s %s", code, &out, &errOut)
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

func TestStatusTableInactivePositionAndPendingRange(t *testing.T) {
	for _, state := range []string{"merged", "dequeued", "not-queued", "queued"} {
		t.Run(state, func(t *testing.T) {
			position, want := 0, "-"
			if state == "queued" {
				position, want = 2, "2"
			}
			until := time.Date(2026, 10, 16, 12, 1, 0, 0, time.UTC)
			response := status.Response{Queue: &status.Queue{Repository: "owner/repo", Base: "main"}, PR: &status.Entry{PR: 1, Position: position, Status: state, HeadSHA: "head"}, Why: &status.Why{Summary: "PR #1: " + state, Events: []status.Event{{ID: 6, EndID: 15, At: until.Add(-time.Minute), Until: &until, Summary: "CI still pending", Count: 5}}}}
			var out bytes.Buffer
			if err := printStatus(&out, response); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(out.String(), "\n")
			fields := strings.Fields(lines[1])
			if len(fields) < 6 || fields[2] != want {
				t.Fatalf("bad position: %s", &out)
			}
			for _, text := range []string{"CI still pending (5 observations)", "2026-10-16T12:00:00Z – 2026-10-16T12:01:00Z", "6–15"} {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("missing %q: %s", text, &out)
				}
			}
		})
	}
}
