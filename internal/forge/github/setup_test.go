package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManifestSetup(t *testing.T) {
	for _, tt := range []struct {
		name, owner, repo, webhook string
	}{
		{"personal polling", "", "acme/project", ""},
		{"organization webhook", "acme", "acme/project", "https://example.test/hooks/github"},
		{"personal webhook without repo", "", "", "https://example.test/hooks/github"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAPI(t)
			f.manifestPEM = string(testPEM(t))
			f.noHookSecret = tt.webhook == ""
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			var out bytes.Buffer
			var address string
			open := func(_ context.Context, start string) {
				address = start
				u, err := url.Parse(start)
				must(t, err)
				if u.Hostname() != "127.0.0.1" || len(u.Query().Get("state")) != 64 {
					t.Fatalf("unsafe callback: %s", start)
				}
				res, err := http.Get(start)
				must(t, err)
				body, err := io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
				if res.StatusCode != 200 || !strings.Contains(string(body), "document.forms[0].submit()") || res.Header.Get("Referrer-Policy") != "no-referrer" {
					t.Fatalf("setup page = %d %s", res.StatusCode, body)
				}
				match := regexp.MustCompile(`name="manifest" value="([^"]*)"`).FindSubmatch(body)
				if len(match) != 2 {
					t.Fatalf("missing manifest: %s", body)
				}
				var manifest appManifest
				must(t, json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &manifest))
				permissions := map[string]string{"contents": "write", "workflows": "write", "pull_requests": "write", "checks": "write", "actions": "write", "statuses": "read", "issues": "read", "metadata": "read", "members": "read"}
				if manifest.Public || !maps.Equal(manifest.Permissions, permissions) || manifest.Name != `My App <test>` {
					t.Fatalf("manifest = %+v", manifest)
				}
				// Polling mode must send no hook at all: GitHub rejects loopback
				// hook URLs even when the hook is inactive.
				if tt.webhook == "" && (manifest.Hook != nil || len(manifest.Events) != 0 || strings.Contains(string(match[1]), "hook_attributes")) {
					t.Fatalf("polling manifest has a hook: %s", match[1])
				}
				if tt.webhook != "" && (manifest.Hook == nil || !manifest.Hook.Active || manifest.Hook.URL != tt.webhook || !slices.Equal(manifest.Events, []string{"pull_request", "pull_request_review", "check_run", "check_suite", "status", "push", "create", "delete", "issue_comment"})) {
					t.Fatalf("webhook manifest = %+v", manifest)
				}
				wantAction := "https://github.com/settings/apps/new?state="
				if tt.owner != "" {
					wantAction = "https://github.com/organizations/" + tt.owner + "/settings/apps/new?state="
				}
				if !strings.Contains(html.UnescapeString(string(body)), wantAction+u.Query().Get("state")) {
					t.Fatalf("wrong registration destination: %s", body)
				}
				callback := manifest.RedirectURL + "?code=one-use-code&state=" + u.Query().Get("state")
				for _, request := range []struct {
					url    string
					status int
				}{
					{manifest.RedirectURL + "?code=one-use-code&state=wrong", 400},
					{manifest.RedirectURL + "?state=" + u.Query().Get("state"), 400},
					{callback, 200}, {callback, 410}, {start, 410},
				} {
					res, err := http.Get(request.url)
					must(t, err)
					must(t, res.Body.Close())
					if res.StatusCode != request.status {
						t.Errorf("callback response = %d, want %d", res.StatusCode, request.status)
					}
				}
			}
			var dir string
			if tt.webhook != "" {
				dir = filepath.Join(t.TempDir(), "App's credentials")
			}
			must(t, setupApp(context.Background(), ManifestOptions{Owner: tt.owner, Repo: tt.repo, Name: `My App <test>`, ConfigDir: dir, WebhookURL: tt.webhook}, &out, f.server.URL+"/api/v3/", open))
			if dir == "" {
				dir = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "openmerge", "openmerge-test")
			}
			info, err := os.Stat(dir)
			must(t, err)
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("directory mode = %o", info.Mode().Perm())
			}
			files := map[string]string{"app-id": "42\n", "slug": "openmerge-test\n", "client-id": "Iv1.test\n", "private-key.pem": f.manifestPEM}
			if tt.webhook != "" {
				files["webhook-secret"] = "setup-webhook-secret"
			} else if _, err := os.Stat(filepath.Join(dir, "webhook-secret")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("polling setup wrote a webhook secret: %v", err)
			}
			for name, want := range files {
				data, err := os.ReadFile(filepath.Join(dir, name))
				must(t, err)
				info, err := os.Stat(filepath.Join(dir, name))
				must(t, err)
				if string(data) != want || info.Mode().Perm() != 0o600 {
					t.Errorf("%s: wrong content or mode %o", name, info.Mode().Perm())
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "client-secret")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unused OAuth client secret was persisted")
			}
			if !strings.Contains(out.String(), "https://github.com/apps/openmerge-test/installations/new") || !strings.Contains(out.String(), "--github-installation-id INSTALLATION_ID") || strings.Contains(out.String(), "setup-webhook-secret") || strings.Contains(out.String(), "BEGIN RSA") {
				t.Fatalf("setup output = %s", out.String())
			}
			repo := tt.repo
			if repo == "" {
				repo = "OWNER/REPO"
			}
			previous := -1
			for _, step := range []string{
				"Install: https://github.com/apps/openmerge-test/installations/new",
				"openmerge setup rulesets --config-dir " + shellQuote(dir) + " --repo " + shellQuote(repo) + "\n",
				"openmerge setup rulesets --config-dir " + shellQuote(dir) + " --repo " + shellQuote(repo) + " --apply",
				"openmerge serve --repos " + shellQuote(repo),
			} {
				index := strings.Index(out.String(), step)
				if index < 0 || index <= previous {
					t.Fatalf("missing or misordered next step %q: %s", step, out.String())
				}
				previous = index
			}
			webhookCommand := `export OPENMERGE_WEBHOOK_SECRET="$(cat ` + shellQuote(filepath.Join(dir, "webhook-secret")) + `)"`
			if tt.webhook != "" {
				if !strings.Contains(out.String(), "Webhook environment (serve requires the secret value):") || !strings.Contains(out.String(), webhookCommand) {
					t.Fatalf("missing webhook value command: %s", out.String())
				}
			} else if strings.Contains(out.String(), "OPENMERGE_WEBHOOK_SECRET") || strings.Contains(out.String(), "Webhook environment") {
				t.Fatalf("polling setup printed webhook instructions: %s", out.String())
			}
			if res, err := http.Get(address); err == nil {
				_ = res.Body.Close()
				t.Fatal("localhost server still running")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.calls["POST app-manifests/one-use-code/conversions"] != 1 {
				t.Fatal("manifest code exchanged more than once")
			}
		})
	}
}

func TestManifestWebhookAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var address string
	var out bytes.Buffer
	err := setupApp(ctx, ManifestOptions{WebhookURL: "https://example.test/hooks/github"}, &out, "http://127.0.0.1:1/", func(_ context.Context, start string) {
		address = start
		res, err := http.Get(start)
		must(t, err)
		body, err := io.ReadAll(res.Body)
		must(t, err)
		must(t, res.Body.Close())
		if !strings.Contains(html.UnescapeString(string(body)), `"active":true`) || !strings.Contains(string(body), "https://example.test/hooks/github") {
			t.Fatalf("webhook manifest = %s", body)
		}
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if res, err := http.Get(address); err == nil {
		_ = res.Body.Close()
		t.Fatal("canceled setup left its listener open")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	err = setupApp(ctx, ManifestOptions{}, io.Discard, "http://127.0.0.1:1/", func(context.Context, string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestManifestRefusesInvalidOptionsAndOverwrite(t *testing.T) {
	for _, opts := range []ManifestOptions{
		{Owner: "../acme"}, {Repo: "bad/repo/path"}, {WebhookURL: "http://example.test/hook"}, {WebhookURL: "https://secret@example.test/"}, {ConfigDir: t.TempDir()},
	} {
		called := false
		err := setupApp(context.Background(), opts, io.Discard, "http://127.0.0.1:1/", func(context.Context, string) { called = true })
		if err == nil || called {
			t.Fatalf("invalid options reached browser: %+v, %v", opts, err)
		}
	}
	dir := filepath.Join(t.TempDir(), "app")
	must(t, os.Symlink(t.TempDir(), dir))
	if _, err := saveCredentials(dir, appCredentials{}); err == nil {
		t.Fatal("overwrote existing directory symlink")
	}
}

func TestManifestConversionErrorsDoNotExposeSecrets(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, _ *http.Request) bool {
		w.WriteHeader(422)
		f.json(w, map[string]any{
			"message":           "one-use-code setup-webhook-secret",
			"errors":            []any{map[string]any{"resource": "secret-resource", "field": "secret-field", "code": "secret-code", "message": "secret-detail"}, "secret-string-detail"},
			"documentation_url": "https://docs.github.com/en/rest/apps/apps?secret=one-use-code",
			"webhook_secret":    "setup-webhook-secret",
			"client_secret":     "unused-oauth-secret",
		})
		return true
	}
	_, err := convertManifest(context.Background(), f.server.URL+"/api/v3/", "one-use-code", true)
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 422 {
		t.Fatalf("unsafe conversion error: %v", err)
	}
	const want = "github API request failed (HTTP 422): GitHub App manifest conversion failed; documentation_url=https://docs.github.com/en/rest/apps/apps"
	if err.Error() != want {
		t.Fatalf("conversion error = %q, want %q", err.Error(), want)
	}
}
