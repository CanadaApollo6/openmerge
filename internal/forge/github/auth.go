// Package github implements the GitHub App forge driver and webhook boundary.
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	gh "github.com/google/go-github/v93/github"
)

// Config contains App credentials. APIBaseURL defaults to https://api.github.com/;
// on GHES use https://HOST/api/v3/. GitBaseURL defaults to https://HOST/.
// HTTPClient's transport must be safe for concurrent use; redirects are disabled
// so neither JWTs nor installation credentials can leave the configured API.
type Config struct {
	AppID                  int64
	PrivateKey             []byte
	APIBaseURL, GitBaseURL string
	HTTPClient             *http.Client
	// Sanitizer shares private-key material and refreshed tokens with API/log redaction.
	Sanitizer *diagnostic.Sanitizer
}

// App owns the signing key and per-installation credential cache.
type App struct {
	id        int64
	key       *rsa.PrivateKey
	api, git  *url.URL
	http      http.Client
	client    *gh.Client
	now       func() time.Time
	mu        sync.Mutex
	tokens    map[int64]*tokenEntry
	drivers   map[int64]*Driver
	slug      string
	sanitizer *diagnostic.Sanitizer
}

type tokenEntry struct {
	mu          sync.Mutex
	token       string
	expires     time.Time
	permissions map[string]string
}

// New loads a PKCS#1 or PKCS#8 RSA PEM private key without exposing it in errors.
func New(cfg Config) (*App, error) {
	cfg.Sanitizer.AddSecrets(string(cfg.PrivateKey))
	if cfg.AppID <= 0 {
		return nil, errors.New("github requires a positive App ID")
	}
	block, rest := pem.Decode(cfg.PrivateKey)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("github requires one RSA PEM private key")
	}
	cfg.Sanitizer.AddSecrets(base64.StdEncoding.EncodeToString(block.Bytes))
	for line := range strings.SplitSeq(string(cfg.PrivateKey), "\n") {
		if !strings.HasPrefix(line, "-----") {
			cfg.Sanitizer.AddSecrets(strings.TrimSpace(line))
		}
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("github requires a valid RSA private key of at least 2048 bits")
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.github.com/"
	}
	api, err := baseURL(cfg.APIBaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.GitBaseURL == "" {
		cfg.GitBaseURL = "https://" + api.Host + "/"
		if api.Host == "api.github.com" {
			cfg.GitBaseURL = "https://github.com/"
		}
	}
	git, err := baseURL(cfg.GitBaseURL)
	if err != nil {
		return nil, err
	}
	a := &App{id: cfg.AppID, key: key, api: api, git: git, now: time.Now, tokens: make(map[int64]*tokenEntry), drivers: make(map[int64]*Driver), sanitizer: cfg.Sanitizer}
	if cfg.HTTPClient != nil {
		a.http = *cfg.HTTPClient
	}
	if a.http.Timeout == 0 {
		a.http.Timeout = 30 * time.Second
	}
	a.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if a.http.Transport == nil {
		a.http.Transport = http.DefaultTransport
	}
	a.client, err = a.newClient(a.jwt)
	if err != nil {
		return nil, safeError(err)
	}
	return a, nil
}

func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid github base URL")
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	secure := u.Scheme == "https" || u.Scheme == "http" && loopback
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !secure {
		return nil, errors.New("github base URL must be HTTPS without credentials, query, or fragment (loopback HTTP allowed for tests)")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	return u, nil
}

func (a *App) jwt(context.Context) (string, error) {
	now := a.now()
	claims, _ := json.Marshal(struct {
		Issuer  string `json:"iss"`
		Issued  int64  `json:"iat"`
		Expires int64  `json:"exp"`
	}{strconv.FormatInt(a.id, 10), now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix()})
	encode := base64.RawURLEncoding.EncodeToString
	payload := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	digest := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("github App signing failed")
	}
	return payload + "." + encode(signature), nil
}

func (a *App) token(ctx context.Context, id int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	entry := a.tokens[id]
	if entry == nil {
		entry = &tokenEntry{}
		a.tokens[id] = entry
	}
	a.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if a.now().Before(entry.expires.Add(-5 * time.Minute)) {
		return entry.token, nil
	}
	token, _, err := a.client.Apps.CreateInstallationToken(ctx, id, nil)
	if err != nil {
		return "", safeError(err)
	}
	a.sanitizer.AddSecrets(token.GetToken())
	if token.GetToken() == "" || !token.GetExpiresAt().After(a.now().Add(5*time.Minute)) {
		return "", errors.New("github returned an empty or short-lived installation token")
	}
	entry.token, entry.expires = token.GetToken(), token.GetExpiresAt().Time
	p := token.GetPermissions()
	entry.permissions = map[string]string{
		"contents": p.GetContents(), "workflows": p.GetWorkflows(), "pull_requests": p.GetPullRequests(),
		"checks": p.GetChecks(), "statuses": p.GetStatuses(), "issues": p.GetIssues(), "metadata": p.GetMetadata(),
	}
	return entry.token, nil
}

// Installation binds a driver to one installation. The server resolves repository
// ownership to an installation before calling it. Identity comes from GET /app.
// Repeated calls share conditional caches, cooldowns, and local write serialization.
func (a *App) Installation(ctx context.Context, id int64) (*Driver, error) {
	if id <= 0 {
		return nil, errors.New("github requires a positive installation ID")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if d := a.drivers[id]; d != nil {
		return d, nil
	}
	if a.slug == "" {
		app, _, err := a.client.Apps.Get(ctx, "")
		if err != nil {
			return nil, safeError(err)
		}
		if app.GetID() != a.id || app.GetSlug() == "" {
			return nil, errors.New("github App identity does not match configuration")
		}
		a.slug = app.GetSlug()
	}
	d := &Driver{app: a, installation: id}
	var err error
	d.client, err = a.newClient(func(ctx context.Context) (string, error) { return a.token(ctx, id) })
	if err != nil {
		return nil, safeError(err)
	}
	a.drivers[id] = d
	return d, nil
}

func (a *App) newClient(token func(context.Context) (string, error)) (*gh.Client, error) {
	client := a.http
	client.Transport = &transport{base: a.http.Transport, token: token, origin: a.api, now: func() time.Time { return a.now() }, cache: newResponseCache(1024, 16<<20)}
	base := a.api.String()
	return gh.NewClient(gh.WithHTTPClient(&client), gh.WithURLs(&base, nil))
}
