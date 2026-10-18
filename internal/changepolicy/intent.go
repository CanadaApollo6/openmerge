package changepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// NormalizeIntent is shared by recorded intent and canonical briefs.
func NormalizeIntent(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
}

// IntentDigest pins PR 6's domain-separated title/body serialization.
func IntentDigest(title, body string) string {
	raw, _ := json.Marshal([]string{"openmerge/intent/v1", NormalizeIntent(title), NormalizeIntent(body)})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
