package serve

import (
	"regexp"
	"strings"
)

var commandBody = regexp.MustCompile(`^@openmerge[ \t](queue(?:[ \t][0-9a-fA-F]{7,40})?|dequeue|refresh)$`)

func command(body string) string {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	match := commandBody.FindStringSubmatch(body)
	if match == nil {
		return ""
	}
	return strings.ReplaceAll(match[1], "\t", " ")
}
