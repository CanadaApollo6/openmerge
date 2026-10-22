package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
)

// Real Git supplies the landed config and CODEOWNERS. An in-memory transport
// exposes those immutable objects to the actual command without network access.
func TestAuthorityAcceptanceImportGoverningEvidenceCannotBeAccepted(t *testing.T) {
	for _, failure := range []string{"owners", "none"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "mock-admin")
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			// The API transport exports immutable objects from an owned real Git
			// origin. Import must bind its report and refusal to those objects.
			dir := t.TempDir()
			remote := filepath.Join(dir, "origin.git")
			git := func(input string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
				cmd.Stdin = strings.NewReader(input)
				data, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, data)
				}
				return strings.TrimSpace(string(data))
			}
			git("", "init", "--bare", "--template=", remote)
			content := "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues: [{name: main, merge_when: ['check-success = ci']}]\n"
			ownerContent := "* @writer\n"
			if failure == "owners" {
				ownerContent = "/README.md @writer\n"
			}
			blob := git(content, "--git-dir="+remote, "hash-object", "-w", "--stdin")
			ownerBlob := git(ownerContent, "--git-dir="+remote, "hash-object", "-w", "--stdin")
			tree := git("100644 blob "+blob+"\t.openmerge.yml\n100644 blob "+ownerBlob+"\tCODEOWNERS\n", "--git-dir="+remote, "mktree")
			commit := git("landed policy\n", "--git-dir="+remote, "commit-tree", tree)
			git("", "--git-dir="+remote, "update-ref", "refs/heads/main", commit)
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
					value = map[string]any{"truncated": false, "tree": []any{map[string]string{"path": ".openmerge.yml", "type": "blob", "mode": "100644", "sha": blob}, map[string]string{"path": "CODEOWNERS", "type": "blob", "mode": "100644", "sha": ownerBlob}}}
				case "/git/blobs/" + blob:
					content := git("", "--git-dir="+remote, "cat-file", "blob", blob) + "\n"
					value = map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
				case "/git/blobs/" + ownerBlob:
					content := git("", "--git-dir="+remote, "cat-file", "blob", ownerBlob) + "\n"
					value = map[string]any{"encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
				case "/users/writer":
					value = map[string]any{"id": 21, "login": "writer", "type": "User"}
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
			if code != 0 && code != 1 {
				t.Fatal("unexpected import exit", code)
			}

			var report github.ProtectionReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("%v: %s", err, &stderr)
			}
			if governingReads == 0 || report.GoverningCommit != commit {
				t.Fatal("governing evidence missing")
			}
			governingFailure := false
			activationGuidance := false
			for _, item := range report.Items {
				if item.Source == "activation" && strings.Contains(item.Message, config.AuthorityActivationPrerequisites) {
					activationGuidance = true
					if item.Blocking {
						t.Fatal("public import retained permanent activation barrier")
					}
					continue
				}
				if item.Source != "governing-evidence" {
					continue
				}
				if item.Source == "governing-evidence" {
					governingFailure = true
				}
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
			if !activationGuidance {
				t.Fatal("public activation guidance absent")
			}
			if governingFailure != (failure == "owners") {
				t.Fatal("governing failure did not reflect actual landed owner coverage", failure)
			}
		})
	}
}
