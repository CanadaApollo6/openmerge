package github

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJWT(t *testing.T) {
	f := newAPI(t)
	for _, offset := range []time.Duration{0, time.Hour, -time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			f.clock = epoch.Add(offset)
			token, err := f.app.jwt(context.Background())
			must(t, err)
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				t.Fatal("JWT does not have three segments")
			}
			decode := func(part string) []byte {
				t.Helper()
				data, err := base64.RawURLEncoding.DecodeString(part)
				must(t, err)
				return data
			}
			var header struct{ Alg, Typ string }
			must(t, json.Unmarshal(decode(parts[0]), &header))
			var claims struct {
				Iss      string
				Iat, Exp int64
			}
			must(t, json.Unmarshal(decode(parts[1]), &claims))
			if header.Alg != "RS256" || header.Typ != "JWT" || claims.Iss != "42" || claims.Iat != f.clock.Add(-time.Minute).Unix() || claims.Exp > f.clock.Add(10*time.Minute).Unix() || claims.Exp-claims.Iat > 600 || claims.Exp <= f.clock.Unix() {
				t.Fatal("invalid JWT claims or algorithm")
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			must(t, rsa.VerifyPKCS1v15(&f.app.key.PublicKey, crypto.SHA256, digest[:], decode(parts[2])))
		})
	}
}
func TestConfig(t *testing.T) {
	key := testPEM(t)
	block, _ := pem.Decode(key)
	parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	must(t, err)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(parsed)
	must(t, err)
	for _, encoding := range [][]byte{key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})} {
		_, err := New(Config{AppID: 42, PrivateKey: encoding})
		must(t, err)
	}
	for _, cfg := range []Config{{AppID: 0, PrivateKey: key}, {AppID: 42, PrivateKey: []byte("secret-invalid-pem")}, {AppID: 42, PrivateKey: append(key, key...)}, {AppID: 42, PrivateKey: key, APIBaseURL: "https://user:secret@example.com/"}, {AppID: 42, PrivateKey: key, APIBaseURL: "http://github.test/"}, {AppID: 42, PrivateKey: key, GitBaseURL: "https://github.test/?secret"}} {
		if _, err := New(cfg); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("configuration error missing or exposes input: %v", err)
		}
	}
}
func TestTokenCacheAndRefresh(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parallel := func(id int64) string {
		t.Helper()
		results := make(chan string, 32)
		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				token, err := f.app.token(ctx, id)
				if err != nil {
					t.Error(err)
				}
				results <- token
			})
		}
		wg.Wait()
		close(results)
		value := ""
		for token := range results {
			if value != "" && value != token {
				t.Fatal("concurrent refresh produced different tokens")
			}
			value = token
		}
		return value
	}
	first := parallel(7)
	if f.tokens != 1 {
		t.Fatalf("token exchanges=%d", f.tokens)
	}
	f.clock = epoch.Add(54 * time.Minute)
	if parallel(7) != first || f.tokens != 1 {
		t.Fatal("token refreshed too early")
	}
	f.clock = epoch.Add(55 * time.Minute)
	second := parallel(7)
	if second == first || f.tokens != 2 {
		t.Fatal("token not refreshed at five-minute margin")
	}
	if parallel(8) == second || f.tokens != 3 {
		t.Fatal("installation credential reused for another installation")
	}
	d, err := f.app.Installation(ctx, 7)
	must(t, err)
	if d != f.driver {
		t.Fatal("installation did not reuse driver")
	}
}
func TestTokenFailureNeverLeaks(t *testing.T) {
	for _, mode := range []string{"api-error", "short-expiry", "empty", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "access_tokens") {
					return false
				}
				switch mode {
				case "api-error":
					w.WriteHeader(401)
					f.json(w, map[string]string{"message": "Bearer secret-installation-token echoed"})
				case "short-expiry":
					f.json(w, map[string]any{"token": "secret-installation-token", "expires_at": f.clock.Add(time.Minute)})
				case "empty":
					f.json(w, map[string]any{"token": "", "expires_at": f.clock.Add(time.Hour)})
				case "invalid-json":
					_, _ = fmt.Fprint(w, "secret-installation-token")
				}
				return true
			}
			_, err := f.app.token(context.Background(), 7)
			if err == nil || strings.Contains(err.Error(), "secret-installation") {
				t.Fatalf("token failure leaked or was accepted: %v", err)
			}
		})
	}
}
func TestRateLimitBackoff(t *testing.T) {
	for _, mode := range []string{"primary", "remaining-zero-success", "secondary", "retry-after-date", "retry-after-seconds", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			requests := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/pulls/1") {
					return false
				}
				requests++
				switch mode {
				case "primary", "remaining-zero-success":
					w.Header().Set("X-RateLimit-Remaining", "0")
					w.Header().Set("X-RateLimit-Reset", fmt.Sprint(f.clock.Add(3*time.Minute).Unix()))
					if mode == "remaining-zero-success" {
						f.json(w, f.prs[1])
						return true
					}
					w.WriteHeader(403)
				case "secondary":
					w.WriteHeader(403)
				case "retry-after-date":
					w.Header().Set("Retry-After", f.clock.Add(3*time.Minute).Format(http.TimeFormat))
					w.WriteHeader(429)
				case "retry-after-seconds":
					w.Header().Set("Retry-After", "180")
					w.WriteHeader(429)
				case "forbidden":
					w.WriteHeader(403)
				}
				message := "secondary rate limit"
				if mode == "primary" || mode == "forbidden" {
					message = "denied"
				}
				f.json(w, map[string]string{"message": message})
				return true
			}
			read := func() error {
				_, _, err := f.driver.client.PullRequests.Get(context.Background(), "acme", "project", 1)
				return safeError(err)
			}
			err := read()
			var rate *RateLimitError
			if mode == "forbidden" {
				var api *APIError
				if !errors.As(err, &api) || api.StatusCode != 403 {
					t.Fatalf("permission failure=%v", err)
				}
				_, _ = f.driver.PullRequest(context.Background(), testRepo, 1)
				if requests != 2 {
					t.Fatal("ordinary forbidden response was rate limited")
				}
				return
			}
			if mode == "remaining-zero-success" {
				must(t, err)
			} else if !errors.As(err, &rate) {
				t.Fatalf("expected typed rate limit: %v", err)
			}
			_, err = f.driver.PullRequest(context.Background(), testRepo, 1)
			if !errors.As(err, &rate) || requests != 1 {
				t.Fatalf("cooldown sent another request: %v, calls=%d", err, requests)
			}
			if (mode == "primary" || strings.HasPrefix(mode, "retry-after")) && rate.RetryAt.Before(epoch.Add(3*time.Minute)) {
				t.Fatal("rate limit ignored retry headers")
			}
			if mode == "secondary" {
				if !rate.Secondary {
					t.Fatal("secondary limit not classified")
				}
				f.clock = rate.RetryAt
				_, err = f.driver.PullRequest(context.Background(), testRepo, 1)
				if !errors.As(err, &rate) || rate.RetryAt.Before(f.clock.Add(2*time.Minute)) {
					t.Fatalf("secondary limit did not back off exponentially: %v", err)
				}
			}
			f.clock = rate.RetryAt.Add(time.Second)
			f.override = nil
			_, err = f.driver.PullRequest(context.Background(), testRepo, 1)
			must(t, err)
		})
	}
}
func TestCredentialsDoNotFollowRedirects(t *testing.T) {
	f := newAPI(t)
	hits := 0
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer other.Close()
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/pulls/1") {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return true
		}
		return false
	}
	_, err := f.driver.PullRequest(context.Background(), testRepo, 1)
	if err == nil || hits != 0 {
		t.Fatal("API followed redirect")
	}
	req, err := http.NewRequestWithContext(context.Background(), "GET", other.URL, nil)
	must(t, err)
	_, err = f.driver.client.Client().Do(req)
	if err == nil || hits != 0 {
		t.Fatal("transport sent a credential outside API origin")
	}
}

func TestCancellationAndTransportErrorScrubbing(t *testing.T) {
	f := newAPI(t)
	_, err := f.app.token(context.Background(), 7)
	must(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = f.driver.Remote(ctx, testRepo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cached remote ignored cancellation: %v", err)
	}
	_, err = f.driver.PullRequest(ctx, testRepo, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("API ignored cancellation: %v", err)
	}
	// A custom transport can echo the Authorization header. Even this diagnostic
	// must not escape through either the public Driver or Remote boundary.
	broken := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("proxy echoed %s", r.Header.Get("Authorization"))
	})
	f.driver.client.Client().Transport.(*transport).base = broken
	_, err = f.driver.PullRequest(context.Background(), testRepo, 1)
	if err == nil || strings.Contains(err.Error(), "secret-installation-") {
		t.Fatalf("transport failure leaked credential: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
