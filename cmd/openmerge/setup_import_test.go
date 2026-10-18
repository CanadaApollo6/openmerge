package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge/github"
)

type importTestTransport func(*http.Request) (*http.Response, error)

func (f importTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSetupImportProtectionOutEndToEnd(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "mock-admin")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	calls := 0
	failRead := false
	http.DefaultTransport = importTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.Host != "api.github.com" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL)
		}
		// This transport handles every request in memory; it cannot reach GitHub.
		path := strings.TrimPrefix(req.URL.Path, "/repos/acme/project")
		status := 200
		var value any
		switch path {
		case "":
			value = map[string]any{"default_branch": "main", "permissions": map[string]bool{"admin": true}}
		case "/rulesets":
			value = []any{}
		case "/branches":
			value = []any{map[string]any{"name": "main", "commit": map[string]string{"sha": "tip"}}}
		case "/git/trees/tip":
			value = map[string]any{"tree": []any{map[string]string{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": "config"}, map[string]string{"path": "CODEOWNERS", "type": "blob", "mode": "100644", "sha": "owners"}}}
		case "/git/blobs/config":
			value = map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("version: 1\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"))}
		case "/git/blobs/owners":
			value = map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("* @alice\n"))}
		case "/collaborators":
			value = []any{map[string]any{"id": 1, "login": "alice", "role_name": "write", "permissions": map[string]bool{"push": true}}}
		case "/commits/tip/check-runs":
			value = map[string]any{"check_runs": []any{}}
		case "/commits/tip/statuses":
			value = []any{}
		case "/branches/main/protection":
			if failRead {
				status = 403
				value = map[string]string{"message": "Forbidden"}
			} else {
				value = map[string]any{"required_pull_request_reviews": map[string]any{"required_approving_review_count": 2}, "required_signatures": map[string]bool{"enabled": false}, "enforce_admins": map[string]bool{"enabled": true}}
			}
		default:
			return nil, fmt.Errorf("unexpected mock route %s", path)
		}
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
	})
	out := filepath.Join(t.TempDir(), "draft.yml")
	var stdout, stderr bytes.Buffer
	code := run([]string{"setup", "import-protection", "--repo", "acme/project", "--json", "--out", out}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "needs attention") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	var report github.ProtectionReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	draft, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(draft) != report.ProposedConfig || !bytes.Contains(draft, []byte("count: 2\n")) || bytes.Contains(draft, []byte("when: {}")) {
		t.Fatalf("saved draft differs: %s", draft)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || calls == 0 {
		t.Fatalf("mode=%o calls=%d", info.Mode().Perm(), calls)
	}
	// A failed read must neither print a partial report nor overwrite a draft.
	failRead = true
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"setup", "import-protection", "--repo", "acme/project", "--json", "--out", out}, &stdout, &stderr)
	after, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || stdout.Len() != 0 || !bytes.Equal(after, draft) {
		t.Fatalf("partial output after failed read: code=%d stdout=%s", code, &stdout)
	}
}
