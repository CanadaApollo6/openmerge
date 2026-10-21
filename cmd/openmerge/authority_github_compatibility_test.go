package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the actual queue-status CLI against the checked-in API projections.
// The serve package owns the GitHub fixture bytes; this test owns the client
// transport, JSON passthrough, and human rendering.
func TestAuthorityGithubProjectionCLI(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		name   string
		golden string
		cli    string
	}{
		{name: "no-approvals", golden: "authority_github_no_approvals.json", cli: "authority_github_no_approvals_cli.txt"},
		{name: "approvals-configured", golden: "authority_github_approvals.json", cli: "authority_github_approvals_cli.txt"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			projectionPath := filepath.Join("..", "..", "internal", "serve", "testdata", tc.golden)
			raw, err := os.ReadFile(projectionPath)
			if err != nil {
				t.Fatal(err)
			}
			var projection map[string]string
			if err := json.Unmarshal(raw, &projection); err != nil {
				t.Fatal(err)
			}
			api := projection["api"]
			if api == "" {
				t.Fatalf("%s has no API projection", projectionPath)
			}
			wantCLI, err := os.ReadFile(filepath.Join("testdata", tc.cli))
			if err != nil {
				t.Fatal(err)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/queues" {
					t.Errorf("unexpected status request: %s %s", r.Method, r.URL)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(api))
			}))
			t.Cleanup(server.Close)

			var stdout, stderr bytes.Buffer
			if code := run([]string{"queue", "status", "--api", server.URL}, &stdout, &stderr); code != 0 {
				t.Fatalf("human CLI exit=%d stderr=%q", code, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), wantCLI) {
				t.Fatalf("human CLI differs:\nwant=%q\ngot=%q", wantCLI, stdout.Bytes())
			}

			stdout.Reset()
			stderr.Reset()
			if code := run([]string{"queue", "status", "--api", server.URL, "--json"}, &stdout, &stderr); code != 0 {
				t.Fatalf("JSON CLI exit=%d stderr=%q", code, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), []byte(api)) {
				t.Fatalf("JSON CLI changed API bytes:\nwant=%q\ngot=%q", api, stdout.Bytes())
			}
		})
	}
}
