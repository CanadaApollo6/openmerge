package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReview4AuditPreservesWorkerQuota(t *testing.T) {
	for _, audit := range []bool{false, true} {
		t.Run(fmt.Sprint(audit), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
			// Real Git history over local smart HTTP; never contact GitHub.
			root := t.TempDir()
			working := filepath.Join(root, "working")
			runGit := func(args ...string) string {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			runGit("init", "--initial-branch=main", working)
			runGit("-C", working, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
			base := runGit("-C", working, "rev-parse", "HEAD")
			runGit("-C", working, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "outside")
			head := runGit("-C", working, "rev-parse", "HEAD")
			if err := os.MkdirAll(filepath.Join(root, "acme"), 0700); err != nil {
				t.Fatal(err)
			}
			runGit("clone", "--bare", working, filepath.Join(root, "acme", "project.git"))
			gitPath, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
				"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=openmerge",
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
			}}
			var mu sync.Mutex
			installationRequests := map[string]int{}
			gitReads := 0
			remaining := 603 // three active reads fit above the existing 600-request reserve
			userReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(r.URL.Path, ".git/") {
					gitReads++
					user, pass, ok := r.BasicAuth()
					if !ok || user != "x-access-token" || pass != "test-token" {
						w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					backend.ServeHTTP(w, r)
					return
				}
				if r.Header.Get("Authorization") == "Bearer test-token" || r.Header.Get("Authorization") == "token test-token" {
					installationRequests[r.URL.Path]++
				}
				w.Header().Set("Content-Type", "application/json")
				var body any
				switch r.URL.Path {
				case "/app":
					body = map[string]any{"id": 42, "slug": "test"}
				case "/app/installations/7/access_tokens":
					body = map[string]any{"token": "test-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)}
				default:
					remaining--
					w.Header().Set("X-RateLimit-Limit", "5000")
					w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
					w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
					switch {
					case r.URL.Path == "/users/test[bot]":
						userReads++
						body = map[string]any{"id": 123, "login": "test[bot]"}
					case strings.HasSuffix(r.URL.Path, "/check-runs"):
						body = map[string]any{"total_count": 0, "check_runs": []any{}}
					case strings.HasSuffix(r.URL.Path, "/statuses"):
						body = []any{}
					case strings.HasSuffix(r.URL.Path, "/check-suites"):
						body = map[string]any{"total_count": 0, "check_suites": []any{}}
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(404)
						return
					}
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			cfg := github.Config{AppID: 42, PrivateKey: pemKey, APIBaseURL: server.URL + "/", GitBaseURL: server.URL + "/"}
			workerApp, err := github.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			driver, err := workerApp.Installation(t.Context(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if audit {
				auditApp, err := github.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				factory := &installationTargets{app: auditApp, options: serveOptions{cache: t.TempDir()}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bundles: make(map[string]*installationBundle)}
				history, err := factory.outsidePushHistory(t.Context(), store.QueueKey{Installation: "7", Repository: "acme/project", BaseBranch: "main"})
				if err != nil {
					t.Fatal(err)
				}
				ref := forge.RepoRef{Path: "acme/project"}
				ancestor, err := history.IsAncestor(t.Context(), ref, base, head)
				if err != nil || !ancestor {
					t.Fatalf("ancestry: %v, %v", ancestor, err)
				}
				commits, truncated, err := history.CommitsBetween(t.Context(), ref, base, head, 100)
				if err != nil || truncated || len(commits) != 1 || commits[0] != head {
					t.Fatalf("history: %v %v %v", commits, truncated, err)
				}
				mu.Lock()
				if len(installationRequests) != 0 || remaining != 603 || gitReads == 0 {
					t.Errorf("audit used installation API quota or skipped Git: paths=%v remaining=%d git=%d", installationRequests, remaining, gitReads)
				}
				mu.Unlock()
			}
			_, err = driver.CheckRuns(forge.WithRequestClass(t.Context(), forge.ActiveRequest), forge.RepoRef{Path: "acme/project"}, "sha")
			mu.Lock()
			if len(installationRequests) != 3 {
				t.Errorf("worker counter missed authenticated paths: %v", installationRequests)
			}
			mu.Unlock()
			t.Logf("audit=%t committer API requests=%d check observation error=%v BackoffUntil=%v", audit, userReads, err, driver.BackoffUntil())
			if err != nil {
				t.Fatalf("audit initialization consumed worker check-observation quota: %v", err)
			}
		})
	}
}
