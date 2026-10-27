package provenance

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
)

const maxDeclarationValue = 161 // longest valid neutral source:session-id

var trailerLine = regexp.MustCompile(`^([A-Za-z0-9-]+)[ \t]*:[ \t]*(.*)$`)
var sessionValue = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[A-Za-z0-9._-]{1,128}$`)
var legacyID = regexp.MustCompile(`^[0-9a-f]{12}$`)

type trailer struct{ key, value string }

// finalTrailers fixes Git's colon separator and default trailer-block rules:
// the last nonblank paragraph must be all trailers/continuations, or contain a
// built-in Signed-off-by and at least 25 percent trailer lines. A title alone is
// never a trailer block. Whitespace continuations unfold with one ASCII space.
// A patch divider (---) ends the message; CRLF, column-zero # comments and
// trailing blank lines are ignored. No host Git configuration is consulted.
func finalTrailers(message string) []trailer {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	var lines []string
	for _, line := range strings.Split(message, "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	for i, line := range lines {
		if line == "---" {
			lines = lines[:i]
			break
		}
	}
	for len(lines) > 0 && strings.Trim(lines[len(lines)-1], " \t\r") == "" {
		lines = lines[:len(lines)-1]
	}
	start := len(lines)
	for start > 0 && strings.Trim(lines[start-1], " \t\r") != "" {
		start--
	}
	if start == 0 {
		return nil
	}
	var out []trailer
	count, recognized := 0, false
	canFold := false
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if canFold {
				out[len(out)-1].value += " " + strings.Trim(line, " \t\r")
				count++
			}
			continue
		}
		m := trailerLine.FindStringSubmatch(line)
		canFold = m != nil
		if m == nil {
			continue
		}
		key := ascii.Lower(m[1])
		recognized = recognized || key == "signed-off-by"
		out = append(out, trailer{key, strings.Trim(m[2], " \t\r")})
		count++
	}
	if count == len(lines)-start || recognized && count*4 >= len(lines)-start {
		for i := range out {
			out[i].value = strings.Trim(out[i].value, " \t\r")
		}
		return out
	}
	return nil
}

// ValidEntireID accepts lowercase legacy hex or canonical uppercase ULIDs.
// The first ULID digit is at most 7: 26 base-32 digits must fit in 128 bits.
func ValidEntireID(value string) bool {
	if legacyID.MatchString(value) {
		return true
	}
	if len(value) != 26 || value[0] < '0' || value[0] > '7' {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return false
		}
	}
	return true
}

// ParseTrailers keeps distinct normalized declarations, including malformed
// recognized values. Coauthor identities are matched separately by Classifier.
func ParseTrailers(message string) []Declaration {
	var out []Declaration
	for _, t := range finalTrailers(message) {
		if t.key != "entire-checkpoint" && t.key != "agent-session" {
			continue
		}
		d := Declaration{Source: "git-trailers", Version: ParserVersion, Kind: t.key, Value: t.value, Outcome: NotRequested, Class: Agent}
		if !utf8.ValidString(t.value) {
			d.Value = ""
			d.ValueDigest = valueDigest(t.value)
			d.Outcome = InvalidEncoding
		} else if len(t.value) > maxDeclarationValue {
			d.Value = ""
			d.ValueDigest = valueDigest(t.value)
			d.Outcome = ValueTooLong
		} else if t.key == "entire-checkpoint" && !ValidEntireID(t.value) || t.key == "agent-session" && !sessionValue.MatchString(t.value) {
			d.Outcome = InvalidValue
		}
		out = append(out, d)
	}
	return canonicalDeclarations(out)
}
