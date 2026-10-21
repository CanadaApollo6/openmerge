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

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
)

// Drive the real command with an in-memory forge transport. A server-chosen
// commit and blob IDs bind every governing read; no request can escape to GitHub.
func TestSetupImportGoverningEvidenceCannotBeAccepted(t *testing.T) {
	for _, failure := range []string{"owners", "identity", "incomplete-tree", "protection", "none"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "mock-admin")
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			commit, blob, ownerBlob := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
			governingReads := 0
			http.DefaultTransport = importTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != "api.github.com" {
					t.Fatalf("unexpected request %s %s", req.Method, req.URL)
				}
				path := strings.TrimPrefix(req.URL.Path, "/repos/acme/project")
				code := 200
				var value any
				switch path {
				case "":
					value = map[string]any{"id": 123, "default_branch": "main", "permissions": map[string]bool{"admin": true}}
				case "/rulesets":
					value = []any{}
				case "/branches":
					value = []any{map[string]any{"name": "main", "commit": map[string]string{"sha": commit}}}
				case "/git/trees/" + commit:
					governingReads++
					value = map[string]any{"truncated": failure == "incomplete-tree", "tree": []any{map[string]string{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": blob}, map[string]string{"path": "CODEOWNERS", "type": "blob", "mode": "100644", "sha": ownerBlob}}}
				case "/git/blobs/" + blob:
					content := "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
					value = map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
				case "/git/blobs/" + ownerBlob:
					content := "* @writer\n"
					if failure == "owners" {
						content = "/README.md @writer\n"
					}
					value = map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
				case "/users/writer":
					if failure == "identity" {
						code = 503
						value = map[string]string{"message": "ghs_SECRET"}
					} else {
						value = map[string]any{"id": 21, "login": "writer", "type": "User"}
					}
				case "/collaborators/writer/permission":
					value = map[string]any{"permission": "write", "user": map[string]any{"id": 21, "login": "writer", "type": "User"}}
				case "/collaborators":
					value = []any{map[string]any{"id": 21, "login": "writer", "role_name": "write", "permissions": map[string]bool{"push": true}}}
				case "/commits/" + commit + "/check-runs":
					value = map[string]any{"check_runs": []any{}}
				case "/commits/" + commit + "/statuses":
					value = []any{}
				case "/branches/main/protection":
					code = 404
					value = map[string]string{"message": "Branch not protected"}
					if failure == "protection" {
						code = 403
						value = map[string]string{"message": "ghs_SECRET"}
					}
				default:
					return nil, fmt.Errorf("unexpected mock route %s", path)
				}
				data, err := json.Marshal(value)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
			})
			var out, stderr bytes.Buffer
			args := []string{"setup", "import-protection", "--repo", "acme/project", "--json"}
			code := run(args, &out, &stderr)
			if code != 1 {
				t.Fatal("public activation barrier lost", code)
			}
			if bytes.Contains(stderr.Bytes(), []byte("ghs_SECRET")) || bytes.Contains(out.Bytes(), []byte("ghs_SECRET")) {
				t.Fatal("secret exposed")
			}
			if failure == "incomplete-tree" || failure == "protection" {
				if out.Len() != 0 {
					t.Fatal("partial report after tree read failure")
				}
				return
			}
			var report github.ProtectionReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("%v: %s", err, &stderr)
			}
			if governingReads == 0 || report.GoverningCommit != commit || report.Ready {
				t.Fatal("governing evidence or public barrier missing")
			}
			found := false
			for _, item := range report.Items {
				if failure == "none" && strings.Contains(item.Message, config.AuthorityActivationUnavailable) {
					found = true
				}
				if item.Source != "governing-evidence" && item.Source != "activation" {
					continue
				}
				found = true
				if !item.Blocking || item.Accepted {
					t.Fatal("waivable governing gate")
				}
				path := filepath.Join(t.TempDir(), "draft.yml")
				if err := os.WriteFile(path, []byte("retain"), 0600); err != nil {
					t.Fatal(err)
				}
				out.Reset()
				stderr.Reset()
				code = run(append(append([]string{}, args...), "--accept", item.ID, "--out", path), &out, &stderr)
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if code != 1 || !strings.Contains(stderr.String(), "not an acceptable policy relaxation") || out.Len() != 0 || string(after) != "retain" {
					t.Fatalf("accept waived governing gate: %d %s", code, &stderr)
				}
			}
			if !found {
				t.Fatal("no governing gate acquired", failure)
			}
		})
	}
}
