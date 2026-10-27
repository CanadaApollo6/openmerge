package provenance

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
)

const maxDeclarationValue = 161 // longest valid neutral source:session-id

var sessionValue = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[A-Za-z0-9._-]{1,128}$`)
var legacyID = regexp.MustCompile(`^[0-9a-f]{12}$`)

type trailer struct{ key, value string }

// gitTrailerPairs decodes Git's colon-separated, unfolded --parse output.
// Block detection, comment handling and continuation unfolding belong to Git.
func gitTrailerPairs(parsed string) []trailer {
	var out []trailer
	for line := range strings.SplitSeq(parsed, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			out = append(out, trailer{ascii.Lower(key), strings.TrimPrefix(value, " ")})
		}
	}
	return out
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

// ParseGitTrailers consumes only the output of git interpret-trailers --parse
// with colon separators, never a raw commit message. It keeps distinct values,
// including malformed recognized declarations. Classifier handles coauthors.
// The caller owns Git invocation, configuration isolation and completeness.
func ParseGitTrailers(parsed string) []Declaration {
	var out []Declaration
	for _, t := range gitTrailerPairs(parsed) {
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
