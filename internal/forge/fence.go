package forge

import (
	"regexp"
	"strings"
)

var (
	landFenceMessage    = regexp.MustCompile(`\Aopenmerge fence (land/[^[:space:]\p{Z}\x00\x85]+) tested=([[:xdigit:]]{40}|[[:xdigit:]]{64}) nonce=[[:xdigit:]]{32}\z`)
	abandonFenceMessage = regexp.MustCompile(`\Aopenmerge fence (abandon/[^[:space:]\p{Z}\x00\x85]+) nonce=[[:xdigit:]]{32}\z`)
)

// ParseFenceMessage recognizes only the exact, single-line landing and
// abandonment formats, allowing Git's terminal newline. Malformed messages and
// the bootstrap fence return empty identity fields.
func ParseFenceMessage(message string) (id, testedSHA string) {
	message = strings.TrimSuffix(message, "\n")
	if match := landFenceMessage.FindStringSubmatch(message); match != nil {
		return match[1], match[2]
	}
	if match := abandonFenceMessage.FindStringSubmatch(message); match != nil {
		return match[1], ""
	}
	return "", ""
}
