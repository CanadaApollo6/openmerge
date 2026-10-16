package diagnostic

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestSanitize(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
		secrets           []string
	}{
		{"plain", "  readable\n diagnostic  ", "readable diagnostic", nil},
		{"bearer NBSP", "Bearer\u00a0opaque-token", "[REDACTED]", nil},
		{"bearer Unicode spaces", "Bearer\u2003\u202fopaque-token", "[REDACTED]", nil},
		{"basic control", "Basic\u0085opaque-token", "[REDACTED]", nil},
		{"bearer format", "Bearer\u202eopaque-token", "[REDACTED]", nil},
		{"known opaque", "problem api-opaque webhook-opaque install-opaque", "problem [REDACTED] [REDACTED] [REDACTED]", []string{"api-opaque", "webhook-opaque", "install-opaque"}},
		{"known whitespace", "key: private\u00a0key\nbody", "key: [REDACTED]", []string{"private key\nbody"}},
		{"encoded", "a%2Bb%2Fc a+b%2Fc a+b/c", "[REDACTED] [REDACTED] [REDACTED]", []string{"a+b/c"}},
		{"overlap", "long-secret long", "[REDACTED] [REDACTED]", []string{"long", "long-secret"}},
		{"PEM", "key -----BEGIN RSA PRIVATE KEY-----\nprivate-body\n-----END RSA PRIVATE KEY----- done", "key [REDACTED] done", nil},
		{"JWT", "token eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOjF9.c2lnbmF0dXJl", "token [REDACTED]", nil},
		{"base64url JWT", "eyJhbGciOiJIUzI1NiJ9.e30.mno_PQR-stu", "[REDACTED]", nil},
		{"unsigned JWT", "eyJhbGciOiJub25lIn0.e30.", "[REDACTED]", nil},
		{"known dotted credential", "abc.def.ghi", "[REDACTED]", []string{"abc.def.ghi"}},
		{"empty", "unchanged", "unchanged", []string{"", "\n"}},
		{"bounded", strings.Repeat("界", 1025), strings.Repeat("界", 1024) + "…", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.value, tc.secrets...); got != tc.want {
				t.Fatalf("Sanitize = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSanitizerLogsAndRefreshedSecrets(t *testing.T) {
	s := NewSanitizer("api-opaque", "hook-opaque", "old-install")
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: s.LogAttr}))
	// The logger is already constructed when a new credential is learned.
	s.AddSecrets("new-install", "private-material")
	for _, value := range []string{"api-opaque", "hook-opaque", "old-install", "new-install", "private-material", "Bearer\u00a0unregistered-token"} {
		t.Run(value, func(t *testing.T) {
			output.Reset()
			logger.Error(value, "detail", value, "error", errors.New(value), slog.Group("nested", "detail", value))
			var row map[string]any
			if err := json.Unmarshal(output.Bytes(), &row); err != nil {
				t.Fatal(err)
			}
			if row["msg"] != "[REDACTED]" || row["detail"] != "[REDACTED]" || row["error"] != "[REDACTED]" || row["nested"].(map[string]any)["detail"] != "[REDACTED]" {
				t.Fatalf("unsafe log: %s", &output)
			}
		})
	}
}

func TestSanitizePreservesNonSecretConfig(t *testing.T) {
	for _, value := range []string{
		"127.0.0.1:8081", "0.0.0.0:8080", "192.0.2.15", "[::1]:8081",
		"docs.github.com", "api.internal.example.test:443", "localhost:54329",
		"abc.def.ghi", "xxx.yyy.zzz", "abc_DEF.ghi-jkl.mno_PQR-stu",
		"v1.2.3", "/etc/openmerge/config.local.yml", "poll_interval=1m0s",
		"e30.config.local", "eyJhbGciOjF9.config.local", "eyJ0eXAiOiJKV1QifQ.config.local",
		"postgres://postgres@localhost:54329/postgres?sslmode=disable",
	} {
		t.Run(value, func(t *testing.T) {
			if got := Sanitize(value); got != value {
				t.Fatalf("non-secret config changed: got %q, want %q", got, value)
			}
		})
	}
}

func TestSanitizerListeningLogPreservesAddress(t *testing.T) {
	s := NewSanitizer("api-opaque", "webhook-opaque", "postgres://postgres@localhost:54329/postgres?sslmode=disable")
	s.AddSecrets("refreshed-installation-token")
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{ReplaceAttr: s.LogAttr}))
	logger.Info("HTTP listening", "listener", "status API", "address", "127.0.0.1:8081", "detail", "refreshed-installation-token")
	var row map[string]any
	if err := json.Unmarshal(output.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row["msg"] != "HTTP listening" || row["listener"] != "status API" || row["address"] != "127.0.0.1:8081" || row["detail"] != "[REDACTED]" {
		t.Fatalf("bad listening log: %s", &output)
	}
}

func TestSanitizerConcurrentRegistration(t *testing.T) {
	s := NewSanitizer("original")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			s.AddSecrets("refreshed")
			if got := s.Sanitize("original refreshed"); got != "[REDACTED] [REDACTED]" {
				t.Errorf("concurrent redaction = %q", got)
			}
		})
	}
	wg.Wait()
}
