package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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
