package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSetupAPICredentialFormsInEveryDiagnosticField(t *testing.T) {
	credentials := []struct {
		name, value string
		known       bool
	}{
		{"admin token", "opaque-admin-token", true},
		{"manifest code", "one-use-code", true},
		{"installation token", "opaque-installation-token", true},
		{"held JWT", "opaque-held-jwt", true},
		{"webhook secret", "setup-webhook-secret", true},
		{"private key bytes", "private-key-bytes-without-armor", true},
		{"bare JWT", "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOjF9.c2lnbmF0dXJl", false},
		{"base64url JWT", "eyJhbGciOiJIUzI1NiJ9.e30.mno_PQR-stu", false},
		{"RSA PEM", "-----BEGIN RSA PRIVATE KEY-----\nprivate-key-material\n-----END RSA PRIVATE KEY-----", false},
		{"PKCS8 PEM", "-----BEGIN PRIVATE KEY-----\nprivate-key-material\n-----END PRIVATE KEY-----", false},
		{"other PEM", "-----BEGIN EC PRIVATE KEY-----\nprivate-key-material\n-----END EC PRIVATE KEY-----", false},
		{"unterminated PEM", "-----BEGIN PRIVATE KEY-----\nprivate-key-material", false},
		{"PAT", "ghp_otherCredential123", false},
		{"OAuth token", "gho_otherCredential123", false},
		{"user token", "ghu_otherCredential123", false},
		{"installation format", "ghs_otherCredential123", false},
		{"refresh token", "ghr_otherCredential123", false},
		{"fine grained PAT", "github_pat_otherCredential123", false},
		{"bearer", "Bearer otherCredential123", false},
		{"basic", "Basic otherCredential123", false},
	}
	for _, credential := range credentials {
		for _, field := range []string{"message", "string error", "resource", "field", "code", "detail message", "wrapped context"} {
			t.Run(credential.name+"/"+field, func(t *testing.T) {
				client := &setupAPI{}
				if credential.known {
					client.secrets = []string{credential.value}
				}
				value := "Useful explanation: " + credential.value
				body := map[string]any{}
				switch field {
				case "wrapped context":
					body["message"] = "Validation failed"
				case "message":
					body["message"] = value
				case "string error":
					body["errors"] = []any{value}
				default:
					key := field
					if key == "detail message" {
						key = "message"
					}
					body["errors"] = []any{map[string]string{key: value}}
				}
				data, err := json.Marshal(body)
				must(t, err)
				err = client.responseError(&http.Response{StatusCode: 422, Body: io.NopCloser(strings.NewReader(string(data)))})
				if field == "wrapped context" {
					err = client.wrapError(fmt.Errorf("unsafe echoed value %s: %w", credential.value, err), "Useful explanation: %s", credential.value)
				}
				err = fmt.Errorf("setting up repository: %w", err)
				var status *APIError
				if !errors.As(err, &status) || status.StatusCode != 422 {
					t.Fatalf("lost HTTP status: %v", err)
				}
				text := err.Error()
				if !strings.Contains(text, "Useful explanation: [REDACTED]") || strings.Contains(text, credential.value) || strings.Contains(text, "private-key-material") {
					t.Fatalf("unsafe diagnostic: %s", text)
				}
				for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
					if strings.Contains(cause.Error(), credential.value) || strings.Contains(cause.Error(), "private-key-material") {
						t.Fatalf("unsafe wrapped cause: %v", cause)
					}
				}
			})
		}
	}
}

func TestSetupAPIDiagnosticsBoundAndCleanFreeText(t *testing.T) {
	client := &setupAPI{secrets: []string{"overlapping", "overlapping-secret"}}
	for _, tt := range []struct{ name, input, want string }{
		{"overlapping credentials", "overlapping-secret overlapping", "[REDACTED] [REDACTED]"},
		{"controls", "useful\x00\n\x1b\u202e explanation", "useful explanation"},
		{"bounded Unicode", strings.Repeat("é", 1100), strings.Repeat("é", 1024) + "…"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"message": tt.input, "errors": []any{map[string]string{"resource": tt.input, "field": tt.input, "code": tt.input, "message": tt.input}, tt.input}})
			must(t, err)
			err = client.responseError(&http.Response{StatusCode: 422, Body: io.NopCloser(strings.NewReader(string(data)))})
			var diagnostic *setupAPIError
			if !errors.As(err, &diagnostic) || len(diagnostic.details) != 2 {
				t.Fatalf("missing diagnostic: %v", err)
			}
			for _, got := range []string{diagnostic.message, diagnostic.details[0].Resource, diagnostic.details[0].Field, diagnostic.details[0].Code, diagnostic.details[0].Message, diagnostic.details[1].Message} {
				if got != tt.want {
					t.Errorf("diagnostic field = %q, want %q", got, tt.want)
				}
			}
			wrapped := client.wrapError(err, "%s", tt.input)
			if !strings.HasPrefix(wrapped.Error(), tt.want+": ") {
				t.Errorf("unsafe wrapped context: %v", wrapped)
			}
		})
	}
}

func TestSetupAPIDiagnosticsRedactCredentials(t *testing.T) {
	const token = "admin+credential/with?punctuation"
	const otherToken = "github_pat_otherCredential123"
	for _, status := range []int{http.StatusForbidden, http.StatusUnprocessableEntity} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("missing admin credential")
				}
				w.WriteHeader(status)
				must(t, json.NewEncoder(w).Encode(map[string]any{
					"message": "Validation failed\n\x1b[31m " + token + " " + otherToken,
					"errors": []any{
						map[string]any{"resource": "RepositoryRuleset", "field": "bypass_actors", "code": "invalid", "message": "Actor rejected: " + url.QueryEscape(token), "value": "never-print-error-value"},
						"Permission denied: " + url.PathEscape(token),
						map[string]any{"resource": token, "field": token, "code": token, "message": "Bearer unrecognized-credential"},
					},
					"documentation_url": "https://docs.github.com/en/rest/repos/rules?token=" + url.QueryEscape(token) + "#" + otherToken,
					"token":             "never-print-unknown-field",
				}))
			}))
			defer server.Close()
			client, err := setupClient(server.URL+"/", token)
			must(t, err)
			var result any
			err = client.request(context.Background(), http.MethodPost, "rulesets", nil, &result)
			var api *APIError
			if !errors.As(err, &api) || api.StatusCode != status {
				t.Fatalf("lost typed HTTP status: %v", err)
			}
			text := err.Error()
			for _, want := range []string{"Validation failed", "RepositoryRuleset", "bypass_actors", "invalid", "Actor rejected", "Permission denied", "[REDACTED]", "documentation_url=https://docs.github.com/en/rest/repos/rules"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing diagnostic %q: %s", want, text)
				}
			}
			for _, secret := range []string{token, url.QueryEscape(token), url.PathEscape(token), otherToken, "unrecognized-credential", "never-print-error-value", "never-print-unknown-field", "?token=", "\n", "\x1b"} {
				if strings.Contains(text, secret) {
					t.Errorf("unsafe diagnostic includes %q: %s", secret, text)
				}
			}
		})
	}
}

func TestSetupAPIDiagnosticsRejectUnsafeBodiesAndLinks(t *testing.T) {
	for _, tt := range []struct {
		name, body string
	}{
		{"not JSON", "<html>raw-response-secret</html>"},
		{"oversized", `{"message":"` + strings.Repeat("x", 64<<10) + `raw-response-secret"}`},
		{"credential link", `{"message":"Denied","documentation_url":"https://raw-response-secret@docs.github.com/en/rest"}`},
		{"foreign link", `{"message":"Denied","documentation_url":"https://example.test/raw-response-secret"}`},
		{"control link", `{"message":"Denied","documentation_url":"https://docs.github.com/\u001braw-response-secret"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := setupClient(server.URL+"/", "")
			must(t, err)
			var result any
			err = client.request(context.Background(), http.MethodGet, "rulesets", nil, &result)
			var api *APIError
			if !errors.As(err, &api) || api.StatusCode != http.StatusForbidden || strings.Contains(err.Error(), "raw-response-secret") || strings.Contains(err.Error(), "documentation_url=") {
				t.Fatalf("unsafe or untyped error: %v", err)
			}
		})
	}
}
