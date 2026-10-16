package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestManifestSetup(t *testing.T) {
	for _, organization := range []string{"", "acme"} {
		t.Run("owner="+organization, func(t *testing.T) {
			f := newAPI(t)
			f.manifestPEM = string(testPEM(t))
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
				if manifest.Public || manifest.Hook.Active || manifest.Hook.URL == "" || manifest.Permissions["checks"] != "write" || manifest.Permissions["statuses"] != "read" || manifest.Permissions["issues"] != "write" || len(manifest.Permissions) != 6 || len(manifest.Events) != 7 || manifest.Name != `My App <test>` {
					t.Fatalf("manifest = %+v", manifest)
				}
				wantAction := "https://github.com/settings/apps/new?state="
				if organization != "" {
					wantAction = "https://github.com/organizations/" + organization + "/settings/apps/new?state="
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
			must(t, setupApp(context.Background(), ManifestOptions{Owner: organization, Repo: "acme/project", Name: `My App <test>`}, &out, f.server.URL+"/api/v3/", open))
			dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "openmerge", "openmerge-test")
			info, err := os.Stat(dir)
			must(t, err)
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("directory mode = %o", info.Mode().Perm())
			}
			for name, want := range map[string]string{"app-id": "42\n", "slug": "openmerge-test\n", "client-id": "Iv1.test\n", "private-key.pem": f.manifestPEM, "webhook-secret": "setup-webhook-secret"} {
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
		f.apiError(w, 422, "one-use-code setup-webhook-secret")
		return true
	}
	_, err := convertManifest(context.Background(), f.server.URL+"/api/v3/", "one-use-code")
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 422 || strings.Contains(err.Error(), "one-use-code") || strings.Contains(err.Error(), "setup-webhook-secret") {
		t.Fatalf("unsafe conversion error: %v", err)
	}
}
