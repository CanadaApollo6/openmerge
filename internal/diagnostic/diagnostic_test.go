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
		{"JWT", "token abc.def.ghi", "token [REDACTED]", nil},
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
