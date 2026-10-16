// Package diagnostic bounds and redacts text crossing an operator-facing boundary.
package diagnostic

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
)

var credentialPattern = regexp.MustCompile(`(?s:-----BEGIN [^-]+-----.*?(?:-----END [^-]+-----|$))|\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)\b|(?i:\b(?:bearer|basic)\s+\S+)`)
var jwtPattern = regexp.MustCompile(`[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// Sanitize redacts credentials and bounds diagnostic text for logs, APIs and terminals.
func Sanitize(value string, knownSecrets ...string) string {
	value = normalize(value)
	secrets := slices.Clone(knownSecrets)
	// Replace longer credentials first so overlapping values cannot leave a suffix.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		if secret != "" {
			for _, encoded := range []string{normalize(secret), url.QueryEscape(secret), url.PathEscape(secret)} {
				if encoded == "" {
					continue
				}
				value = strings.ReplaceAll(value, encoded, "[REDACTED]")
			}
		}
	}
	value = credentialPattern.ReplaceAllString(value, "[REDACTED]")
	value = jwtPattern.ReplaceAllStringFunc(value, func(candidate string) string {
		// A JWT's header and claims are both base64url-encoded JSON objects.
		// Dots alone also occur in addresses, hostnames, paths and versions,
		// which do not decode to two objects. Any object qualifies, whatever its
		// keys, so unusual but valid headers cannot opt out of redaction.
		header, rest, _ := strings.Cut(candidate, ".")
		claims, _, _ := strings.Cut(rest, ".")
		if !jsonObject(header) || !jsonObject(claims) {
			return candidate
		}
		return "[REDACTED]"
	})
	runes := []rune(value)
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return string(runes)
}

func jsonObject(segment string) bool {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segment, "="))
	var object map[string]json.RawMessage
	return err == nil && json.Unmarshal(data, &object) == nil && object != nil
}

func normalize(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// Sanitizer shares process credentials across API and log boundaries, including
// tokens learned after startup. Retained credentials also redact old diagnostics.
type Sanitizer struct {
	mu      sync.RWMutex
	secrets []string
}

// NewSanitizer registers the credentials available at startup.
func NewSanitizer(secrets ...string) *Sanitizer {
	s := &Sanitizer{}
	s.AddSecrets(secrets...)
	return s
}

// AddSecrets registers credentials before their first use. A nil receiver is allowed.
func (s *Sanitizer) AddSecrets(secrets ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, secret := range secrets {
		if secret != "" && !slices.Contains(s.secrets, secret) {
			s.secrets = append(s.secrets, secret)
		}
	}
}

// Sanitize uses all registered secrets; a nil receiver still redacts patterns.
func (s *Sanitizer) Sanitize(value string) string {
	if s == nil {
		return Sanitize(value)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Sanitize(value, s.secrets...)
}

// LogAttr redacts messages, string attributes and error values in slog handlers.
func (s *Sanitizer) LogAttr(_ []string, attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if attr.Value.Kind() == slog.KindString {
		attr.Value = slog.StringValue(s.Sanitize(attr.Value.String()))
	} else if err, ok := attr.Value.Any().(error); ok {
		attr.Value = slog.StringValue(s.Sanitize(err.Error()))
	}
	return attr
}
