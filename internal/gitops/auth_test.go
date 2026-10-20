package gitops

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func writeExecutable(t *testing.T, path, script string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o700))
}

func TestHTTPAskpassKeepsCredentialsOutOfArgvAndFiles(t *testing.T) {
	f := newFixture(t)
	gitCommand(t, f.remote, "", nil, "config", "http.receivepack", "true")
	gitPath, err := exec.LookPath("git")
	must(t, err)
	username, password := "token-user", "p@ss:/token?secret"
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=" + filepath.Dir(f.remote), "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=openmerge",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, req)
	}))
	defer server.Close()
	for _, inURL := range []bool{false, true} {
		t.Run(fmt.Sprintf("userinfo=%v", inURL), func(t *testing.T) {
			remote, err := url.Parse(server.URL + "/remote.git")
			must(t, err)
			auth := Auth{Username: username, Password: password}
			if inURL {
				remote.User, auth = url.UserPassword(username, password), Auth{}
			}
			workDir := filepath.Join(t.TempDir(), "cache with spaces")
			r, err := New(context.Background(), Config{WorkDir: workDir, HeadRef: headRef,
				Remote: func(context.Context, forge.RepoRef) (string, Auth, error) { return remote.String(), auth, nil },
				Now:    func() time.Time { return epoch },
			})
			must(t, err)
			record := filepath.Join(t.TempDir(), "argv")
			wrapper := filepath.Join(t.TempDir(), "git")
			writeExecutable(t, wrapper, "printf '%s\\n' \"$@\" >> "+shellQuote(record)+"\nexec "+shellQuote(gitPath)+" \"$@\"\n")
			r.git = wrapper
			trace := filepath.Join(t.TempDir(), "trace")
			t.Setenv("GIT_TRACE", trace)
			t.Setenv("GIT_TRACE_CURL", trace)
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
			t.Setenv("GIT_CONFIG_VALUE_0", "!echo unsafe-helper > "+trace)
			sha, err := r.ReadRef(context.Background(), testRepo, "main")
			must(t, err)
			if sha != f.base {
				t.Fatal("authenticated HTTP read returned the wrong base")
			}
			_, err = r.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{
				Ref: "openmerge/queue/main/http", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: "http"}, forge.PushOptions{})
			must(t, err)
			argv, err := os.ReadFile(record)
			must(t, err)
			for _, secret := range []string{username, password, url.QueryEscape(password), url.PathEscape(password)} {
				if strings.Contains(string(argv), secret) {
					t.Fatalf("credential appeared in git argv: %q", secret)
				}
				must(t, filepath.WalkDir(workDir, func(path string, entry os.DirEntry, err error) error {
					if err != nil || entry.IsDir() {
						return err
					}
					data, err := os.ReadFile(path)
					if err == nil && strings.Contains(string(data), secret) {
						return errors.New("credential persisted in git cache")
					}
					return err
				}))
			}
			if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("git inherited tracing or credential config: %v", err)
			}
		})
	}
}

func TestErrorsScrubCredentialsAndURLUserinfo(t *testing.T) {
	f := newFixture(t)
	secret := Auth{Username: "private-user", Password: "private:password@value"}
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, `printf '%s\n' "$OPENMERGE_GIT_USERNAME $OPENMERGE_GIT_PASSWORD https://someone:else@example.test/repo" >&2
exit 1
`)
	f.g.git = wrapper
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return f.remote, secret, nil
	}
	_, err := f.g.ReadRef(context.Background(), testRepo, "main")
	if err == nil {
		t.Fatal("failing git command succeeded")
	}
	for _, value := range []string{secret.Username, secret.Password, "someone:else"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("git error leaked %q", value)
		}
	}
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return "", secret, fmt.Errorf("https://someone:else@example.test/repo: %s", secret.Password)
	}
	_, err = f.g.ReadRef(context.Background(), testRepo, "main")
	if err == nil || strings.Contains(err.Error(), secret.Password) || strings.Contains(err.Error(), "someone:else") {
		t.Fatalf("remote provider error was not scrubbed: %v", err)
	}
}

func TestVersionRequirementAndSingleConstructionCheck(t *testing.T) {
	for _, version := range []string{"2.37.9", "1.99.0", "unknown"} {
		t.Run(version, func(t *testing.T) {
			bin := t.TempDir()
			writeExecutable(t, filepath.Join(bin, "git"), "printf '%s\\n' "+shellQuote("git version "+version)+"\n")
			t.Setenv("PATH", bin)
			_, err := New(context.Background(), Config{WorkDir: t.TempDir(), HeadRef: headRef,
				Remote: func(context.Context, forge.RepoRef) (string, Auth, error) { return "unused", Auth{}, nil }})
			if err == nil || !strings.Contains(err.Error(), "requires git >= 2.38") {
				t.Fatalf("old/invalid version error = %v", err)
			}
		})
	}
	f := newFixture(t)
	gitPath, err := exec.LookPath("git")
	must(t, err)
	bin, record := t.TempDir(), filepath.Join(t.TempDir(), "argv")
	writeExecutable(t, filepath.Join(bin, "git"), "printf '%s\\n' \"$@\" >> "+shellQuote(record)+"\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := f.client(t, 0)
	for range 2 {
		_, err := r.ReadRef(context.Background(), testRepo, "main")
		must(t, err)
	}
	data, err := os.ReadFile(record)
	must(t, err)
	if count := strings.Count(string(data), "--version\n"); count != 1 {
		t.Fatalf("checked git version %d times, want once", count)
	}
}

func TestCancellationAndInfrastructureErrors(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.g.ReadRef(ctx, testRepo, "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		cancel()
		return "", Auth{}, ctx.Err()
	}
	if _, err := f.g.ReadRef(ctx, testRepo, "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled remote resolution = %v", err)
	}
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return filepath.Join(t.TempDir(), "missing.git"), Auth{}, nil
	}
	_, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{
		Ref: "openmerge/queue/main/missing", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: "missing"}, forge.PushOptions{})
	if err == nil || errors.Is(err, forge.ErrConflict) || errors.Is(err, forge.ErrLeaseRejected) {
		t.Fatalf("infrastructure error misclassified: %v", err)
	}
}

func TestFinalPushRefreshesCredentials(t *testing.T) {
	for _, operation := range []string{"build", "land", "advance"} {
		for _, failRefresh := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail-refresh=%v", operation, failRefresh), func(t *testing.T) {
				f := newFixture(t)
				ctx := context.Background()
				initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
				must(t, err)
				tested := f.build(forge.Merge, f.prs[:1], "refresh")
				gitPath, err := exec.LookPath("git")
				must(t, err)
				marker := filepath.Join(t.TempDir(), "pushed")
				wrapper := filepath.Join(t.TempDir(), "git")
				writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = push ]; then\n[ \"$OPENMERGE_GIT_PASSWORD\" = fresh-token ] || exit 90\nprintf fresh > "+shellQuote(marker)+"\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
				f.g.git = wrapper
				calls := 0
				f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
					calls++
					if calls == 1 {
						return f.remote, Auth{Password: "expired-token"}, nil
					}
					if failRefresh {
						return "", Auth{}, fmt.Errorf("token refresh failed: %w", ErrAuth)
					}
					return f.remote, Auth{Password: "fresh-token"}, nil
				}
				switch operation {
				case "build":
					_, err = f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/refresh-final", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: "refresh-final"}, forge.PushOptions{})
				case "land":
					_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
						Fence: testFence, ExpectedFence: initial, NextFenceID: "land/refresh", PRs: f.prs[:1]}, forge.PushOptions{})
				default:
					_, err = f.g.AdvanceFence(ctx, testRepo, testFence, initial, "abandon/refresh", forge.PushOptions{})
				}
				if calls != 2 {
					t.Fatalf("resolved credentials %d times, want operation start and final push", calls)
				}
				if failRefresh {
					if !errors.Is(err, ErrAuth) {
						t.Fatalf("refresh failure lost ErrAuth: %v", err)
					}
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Fatal("push ran after credential refresh failed")
					}
					if gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main") != f.base || gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence) != initial {
						t.Fatal("failed refresh changed refs")
					}
				} else {
					must(t, err)
					if data, err := os.ReadFile(marker); err != nil || string(data) != "fresh" {
						t.Fatalf("push did not use refreshed credentials: %v", err)
					}
				}
			})
		}
	}
}

func TestAuthenticationErrors(t *testing.T) {
	f := newFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return server.URL + "/repo.git", Auth{Username: "user", Password: "bad-token"}, nil
	}
	_, err := f.g.ReadRef(context.Background(), testRepo, "main")
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "bad-token") {
		t.Fatalf("HTTP authentication failure was not classified and scrubbed: %v", err)
	}
	for _, diagnostic := range []string{"Permission denied (publickey).", "HTTP Basic: Access denied", "The requested URL returned error: 403"} {
		s := &session{}
		if !errors.Is(s.failure("fetch", diagnostic, errors.New("failed")), ErrAuth) {
			t.Fatalf("authentication diagnostic not classified: %s", diagnostic)
		}
	}
}

func TestEmptyDiagnostics(t *testing.T) {
	f := newFixture(t)
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "exit 42\n")
	f.g.git = wrapper
	s := &session{r: f.g}
	_, err := s.command(context.Background(), "", "fetch")
	if err == nil || err.Error() != "git fetch: exit status 42" {
		t.Fatalf("empty diagnostic error = %v", err)
	}
	writeExecutable(t, wrapper, "printf 'transport refused\\n'\nexit 42\n")
	_, err = s.command(context.Background(), "", "fetch")
	if err == nil || err.Error() != "git fetch: transport refused: exit status 42" {
		t.Fatalf("stdout diagnostic was discarded: %v", err)
	}
}

func TestNewErrorsIncludeWorkDir(t *testing.T) {
	for _, kind := range []string{"configuration", "identity", "filesystem"} {
		t.Run(kind, func(t *testing.T) {
			workDir := filepath.Join(t.TempDir(), "work-dir")
			cfg := Config{WorkDir: workDir, HeadRef: headRef,
				Remote: func(context.Context, forge.RepoRef) (string, Auth, error) { return "unused", Auth{}, nil }}
			switch kind {
			case "configuration":
				cfg.Remote = nil
			case "identity":
				cfg.Author = Identity{Name: "missing email"}
			case "filesystem":
				must(t, os.WriteFile(workDir, nil, 0o600))
			}
			_, err := New(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), workDir) {
				t.Fatalf("New did not identify WorkDir: %v", err)
			}
		})
	}
}
