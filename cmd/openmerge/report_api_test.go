package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/status"
)

func TestReportCommandsHaveReads(t *testing.T) {
	excluded := map[string]string{
		"ci-doctor": "ADR 0017 serves CI doctor snapshots in a later rollout slice.",
	}
	for _, name := range reportCommands {
		if slices.ContainsFunc(status.Reads(), func(r status.Read) bool { return r.Name == name }) {
			if excluded[name] != "" {
				t.Errorf("stale exclusion: %s", name)
			}
			continue
		}
		if excluded[name] == "" {
			t.Errorf("report %s has no API read or justified exclusion", name)
		}
	}
	for name := range excluded {
		if !slices.Contains(reportCommands, name) {
			t.Errorf("unknown excluded command: %s", name)
		}
	}
}

func TestReportSavingsAPI(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://invalid:should-not-be-used@localhost:1/missing")
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("read-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/report-savings.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.EscapedPath() != "/v1/queues/fixture/app/release%2Fv1/reports/savings" || r.Header.Get("Authorization") != "Bearer read-secret" || r.URL.Query().Get("full-suite-minutes") != "20" || r.URL.Query().Get("since") != "2026-10-01T00:00:00Z" || r.URL.Query().Get("until") != "2026-10-01T01:00:00Z" {
			t.Errorf("unexpected request: %s %s %v", r.Method, r.URL, r.Header)
		}
		_, _ = fmt.Fprintf(w, `{"version":1,"savings":%s}`, body)
	}))
	defer server.Close()
	args := []string{"report", "savings", "--api", server.URL, "--api-token-file", tokenFile, "--repository", "fixture/app", "--base", "release/v1", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-01T01:00:00Z", "--full-suite-minutes", "20"}
	var out, stderr bytes.Buffer
	if code := run(append(slices.Clone(args), "--json"), &out, &stderr); code != 0 || !bytes.Equal(out.Bytes(), body) {
		t.Fatalf("%d %s %s", code, out.String(), stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := run(args, &out, &stderr); code != 0 || !strings.Contains(out.String(), "CI runs") {
		t.Fatalf("%d %s %s", code, out.String(), stderr.String())
	}
	for _, extra := range [][]string{{"--database-url", "postgres://secret"}, {"--installation", "1"}, {"--lane", "fast"}, {"--api", ""}, {"--api", "http://user:secret@localhost"}, {"--api", "http://localhost/path"}, {"--full-suite-minutes", "NaN"}, {"--full-suite-minutes", "Inf"}} {
		out.Reset()
		stderr.Reset()
		if code := run(append(slices.Clone(args), extra...), &out, &stderr); code != 2 || out.Len() != 0 || strings.Contains(stderr.String(), "postgres://secret") {
			t.Fatalf("%v: %d %s %s", extra, code, &out, &stderr)
		}
	}
}

func TestReportSavingsAPIErrorsAndLimits(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "read-secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		name         string
		status, exit int
		body         string
	}{
		{"missing", 404, 3, `{"version":1,"error":"not-found","message":"missing"}`},
		{"unauthorized", 401, 4, `{"version":1,"error":"unauthorized","message":"read-secret"}`},
		{"forbidden", 403, 4, ""},
		{"ambiguous", 409, 5, `{"version":1,"error":"ambiguous-queue","message":"multiple lanes"}`},
		{"invalid", 400, 2, `{"version":1,"error":"invalid-request","message":"bad window"}`},
		{"unavailable", 503, 1, `{"version":1,"error":"unavailable","message":"deadline"}`},
		{"large-error", 500, 1, strings.Repeat("x", 8193)},
		{"redirect", 302, 1, ""},
		{"bad-json", 200, 1, `{"version":1}`},
		{"wrong-version", 200, 1, `{"version":2,"savings":{"version":1}}`},
		{"large", 200, 1, strings.Repeat(" ", 32<<20) + "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			followed := false
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					followed = true
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer s.Close()
			var out, stderr bytes.Buffer
			code := run([]string{"report", "savings", "--api", s.URL, "--repository", "fixture/app", "--since", "2026-10-01T00:00:00Z", "--until", "2026-10-02T00:00:00Z", "--json"}, &out, &stderr)
			if code != tc.exit || out.Len() != 0 || followed || strings.Contains(stderr.String(), "read-secret") {
				t.Fatalf("%d %s %s followed=%v", code, &out, &stderr, followed)
			}
		})
	}
}
