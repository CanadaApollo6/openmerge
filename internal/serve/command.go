package serve

import (
	"regexp"
	"strings"
)

var commandBody = regexp.MustCompile(`^@openmerge[ \t](queue(?:[ \t](?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64}))?|dequeue|refresh)$`)

func command(body string) string {
	// Only ASCII whitespace is trimmed, so NBSP or other invisible padding
	// makes the body a non-command.
	body = strings.Trim(strings.ReplaceAll(body, "\r\n", "\n"), " \t\r\n")
	match := commandBody.FindStringSubmatch(body)
	if match == nil {
		return ""
	}
	return strings.ReplaceAll(match[1], "\t", " ")
}
