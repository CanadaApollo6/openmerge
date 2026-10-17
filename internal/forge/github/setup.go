package github

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ManifestOptions configures a private, owner-installed App. An empty Owner uses
// the signed-in personal account. ConfigDir must be a new directory.
type ManifestOptions struct {
	Owner, Repo, Name, ConfigDir, WebhookURL string
}

type appManifest struct {
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	RedirectURL string            `json:"redirect_url"`
	Public      bool              `json:"public"`
	Permissions map[string]string `json:"default_permissions"`
	Events      []string          `json:"default_events,omitempty"`
	Hook        *manifestHook     `json:"hook_attributes,omitempty"`
}

type manifestHook struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

type appCredentials struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
}

var manifestPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><title>Set up OpenMerge</title>
<body><p>Continue on GitHub to create your private OpenMerge App.</p>
<form action="{{.Action}}" method="post"><input type="hidden" name="manifest" value="{{.Manifest}}"><button>Continue to GitHub</button></form>
<script>document.forms[0].submit()</script></body></html>`))

// SetupApp completes GitHub's manifest exchange with a ten-minute deadline. The
// browser receives no credentials; only the one-use code reaches localhost.
func SetupApp(ctx context.Context, opts ManifestOptions, out io.Writer) error {
	return setupApp(ctx, opts, out, "https://api.github.com/", func(ctx context.Context, address string) {
		cmd := exec.CommandContext(ctx, "xdg-open", address)
		if cmd.Start() == nil {
			go func() { _ = cmd.Wait() }()
		}
	})
}

func setupApp(ctx context.Context, opts ManifestOptions, out io.Writer, api string, open func(context.Context, string)) error {
	if opts.Owner != "" && !simpleName(opts.Owner) {
		return errors.New("--owner must be a GitHub organization login")
	}
	if opts.Repo != "" {
		if _, _, err := splitRepoPath(opts.Repo); err != nil {
			return err
		}
	}
	if opts.WebhookURL != "" {
		u, err := url.Parse(opts.WebhookURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return errors.New("--webhook-url must be a public HTTPS URL without credentials or fragment")
		}
	}
	if opts.ConfigDir != "" {
		if _, err := os.Lstat(opts.ConfigDir); !errors.Is(err, os.ErrNotExist) {
			return errors.New("--config-dir must not already exist")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address := "http://" + listener.Addr().String()
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		_ = listener.Close()
		return err
	}
	state := hex.EncodeToString(stateBytes)
	name := opts.Name
	if name == "" {
		name = "OpenMerge"
	}
	// GitHub validates a manifest hook URL even when the hook is inactive and
	// rejects loopback addresses, so polling mode sends no hook and no events.
	var hook *manifestHook
	var events []string
	if opts.WebhookURL != "" {
		hook = &manifestHook{URL: opts.WebhookURL, Active: true}
		events = []string{"pull_request", "pull_request_review", "check_run", "check_suite", "status", "push", "create", "delete", "issue_comment"}
	}
	manifest, err := json.Marshal(appManifest{
		Name: name, URL: "https://github.com/CanadaApollo6/openmerge", RedirectURL: address + "/callback",
		Permissions: map[string]string{"contents": "write", "workflows": "write", "pull_requests": "write", "checks": "write", "statuses": "read", "issues": "read", "metadata": "read"},
		Events:      events, Hook: hook,
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	action := "https://github.com/settings/apps/new"
	if opts.Owner != "" {
		action = "https://github.com/organizations/" + opts.Owner + "/settings/apps/new"
	}
	action += "?state=" + state
	codes := make(chan string, 1)
	var used atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; form-action https://github.com; frame-ancestors 'none'")
		if r.Host != listener.Addr().String() || r.Method != http.MethodGet || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "invalid setup request or state", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/":
			if used.Load() {
				http.Error(w, "setup code already received", http.StatusGone)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = manifestPage.Execute(w, struct{ Action, Manifest string }{action, string(manifest)})
		case "/callback":
			code := r.URL.Query().Get("code")
			if !simpleName(code) || len(code) > 512 {
				http.Error(w, "missing or invalid manifest code", http.StatusBadRequest)
				return
			}
			if !used.CompareAndSwap(false, true) {
				http.Error(w, "setup code already received", http.StatusGone)
				return
			}
			codes <- code
			_, _ = io.WriteString(w, "Code received. Return to the terminal to finish setup.\n")
		default:
			http.NotFound(w, r)
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second}
	defer func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
	}()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	start := address + "/?state=" + state
	if _, err := fmt.Fprintln(out, "Open this URL on this machine within 10 minutes:\n"+start); err != nil {
		return err
	}
	open(ctx, start)
	var code string
	select {
	case <-ctx.Done():
		return fmt.Errorf("GitHub App setup stopped: %w", ctx.Err())
	case err := <-serverErrors:
		return err
	case code = <-codes:
	}
	credentials, err := convertManifest(ctx, api, code, opts.WebhookURL != "")
	if err != nil {
		return err
	}
	dir, err := saveCredentials(opts.ConfigDir, credentials)
	if err != nil {
		return err
	}
	repo := opts.Repo
	if repo == "" {
		repo = "OWNER/REPO"
	}
	var steps strings.Builder
	fmt.Fprintf(&steps, "Saved App credentials in %s\n\n1. Install: https://github.com/apps/%s/installations/new\nSelect Only select repositories and include %s.\nFind the installation ID at the end of Settings → Applications → Installed GitHub Apps → Configure (…/settings/installations/12345).\n", dir, credentials.Slug, repo)
	fmt.Fprintf(&steps, "\n2. Preview repository rulesets, then apply the changes. Set GITHUB_TOKEN to a separate repository-admin token (Administration read for preview, write for apply):\n  openmerge setup rulesets --config-dir %s --repo %s\n  openmerge setup rulesets --config-dir %s --repo %s --apply\n", shellQuote(dir), shellQuote(repo), shellQuote(dir), shellQuote(repo))
	fmt.Fprintln(&steps, "\n3. Start the server after resolving all reported ruleset problems and checking classic branch protection (see openmerge serve --help when available). Replace DEFAULT_BRANCH with the repository's default branch:")
	if opts.WebhookURL != "" {
		fmt.Fprintf(&steps, "Webhook environment (serve requires the secret value):\n  export OPENMERGE_WEBHOOK_SECRET=\"$(cat %s)\"\n", shellQuote(filepath.Join(dir, "webhook-secret")))
	}
	fmt.Fprintf(&steps, "  OPENMERGE_DATABASE_URL=POSTGRES_URL openmerge serve --repos %s --base DEFAULT_BRANCH --github-app-id %d --github-installation-id INSTALLATION_ID --github-private-key %s\n", shellQuote(repo), credentials.ID, shellQuote(filepath.Join(dir, "private-key.pem")))
	_, err = io.WriteString(out, steps.String())
	return err
}

func simpleName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func convertManifest(ctx context.Context, api, code string, webhook bool) (appCredentials, error) {
	var credentials appCredentials
	client, err := setupClient(api, "")
	if err != nil {
		return credentials, err
	}
	client.secrets = append(client.secrets, code)
	client.manifestConversion = true
	err = client.request(ctx, http.MethodPost, "app-manifests/"+code+"/conversions", nil, &credentials)
	if err != nil {
		return appCredentials{}, err
	}
	if !simpleName(credentials.Slug) || credentials.ClientID == "" || webhook && credentials.WebhookSecret == "" {
		return appCredentials{}, errors.New("GitHub manifest response is missing App credentials")
	}
	if _, err := New(Config{AppID: credentials.ID, PrivateKey: []byte(credentials.PEM)}); err != nil {
		return appCredentials{}, errors.New("GitHub manifest response contains invalid App ID or private key")
	}
	return credentials, nil
}

func saveCredentials(dir string, c appCredentials) (string, error) {
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(root, "openmerge", c.Slug)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", fmt.Errorf("refusing to overwrite App directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	files := map[string]string{
		"app-id": strconv.FormatInt(c.ID, 10) + "\n", "slug": c.Slug + "\n", "client-id": c.ClientID + "\n",
		"private-key.pem": c.PEM,
	}
	if c.WebhookSecret != "" {
		files["webhook-secret"] = c.WebhookSecret
	}
	for name, value := range files {
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", err
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return "", err
		}
		_, writeErr := io.WriteString(file, value)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return "", err
		}
	}
	complete = true
	return dir, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
