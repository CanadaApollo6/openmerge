// Package diagnostic bounds and redacts text crossing an operator-facing boundary.
package diagnostic

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var credentialPattern = regexp.MustCompile(`(?s:-----BEGIN [^-]+-----.*?(?:-----END [^-]+-----|$))|[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)\b|(?i:\b(?:bearer|basic)\s+\S+)`)

// Sanitize redacts credentials and bounds diagnostic text for logs, APIs and terminals.
func Sanitize(value string, knownSecrets ...string) string {
	value = credentialPattern.ReplaceAllString(value, "[REDACTED]")
	secrets := slices.Clone(knownSecrets)
	// Replace longer credentials first so overlapping values cannot leave a suffix.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, secret := range secrets {
		if secret != "" {
			for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				value = strings.ReplaceAll(value, encoded, "[REDACTED]")
			}
		}
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return string(runes)
}
