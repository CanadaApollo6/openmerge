package classification

import (
	"fmt"
	"regexp"
	"strings"
)

// Git wildmatch operates on bytes. Encode each UTF-8 byte as one rune before
// using Go's regexp engine, so ? and ranges do not consume a whole code point.
type treeMatcher struct{ expression *regexp.Regexp }

func (m *treeMatcher) MatchString(p string) bool { return m.expression.MatchString(byteRunes(p)) }
func byteRunes(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return r > 127 }) < 0 {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		out.WriteRune(rune(s[i]))
	}
	return out.String()
}

// treePattern is deliberately independent of OpenMerge's whole-path dialect.
// CODEOWNERS propagates literal directory entries; attributes never do so.
func treePattern(pattern string, owners bool) (*treeMatcher, error) {
	if pattern == "" || strings.HasPrefix(pattern, "!") || owners && (strings.HasPrefix(pattern, `\#`) || strings.ContainsAny(pattern, "[]")) {
		return nil, fmt.Errorf("unsupported pattern %q", pattern)
	}
	anchored := strings.HasPrefix(pattern, "/")
	directory := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimPrefix(strings.TrimSuffix(pattern, "/"), "/")
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	prefix := "^"
	if !anchored && !strings.Contains(pattern, "/") {
		prefix = "(?:^|/)"
	}
	var out strings.Builder
	out.WriteString("(?s)" + prefix)
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			i++
			if i == len(pattern) {
				return nil, fmt.Errorf("trailing escape")
			}
			out.WriteString(regexp.QuoteMeta(byteRunes(pattern[i : i+1])))
		case '*':
			start := i
			for i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
			}
			double := i-start+1 == 2 && (start == 0 || pattern[start-1] == '/')
			if double && i+1 < len(pattern) && pattern[i+1] == '/' {
				out.WriteString("(?:[^/]+/)*")
				i++
			} else if double && i+1 == len(pattern) {
				out.WriteString(".*")
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		case '[':
			end := i + 1
			if end < len(pattern) && (pattern[end] == '!' || pattern[end] == '^') {
				end++
			}
			if end < len(pattern) && pattern[end] == ']' {
				end++
			}
			for end < len(pattern) && pattern[end] != ']' {
				end++
			}
			if end == len(pattern) {
				return nil, fmt.Errorf("unterminated range")
			}
			class := pattern[i+1 : end]
			if strings.ContainsAny(class, "[/\\") {
				return nil, fmt.Errorf("unsupported range")
			}
			if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
				body := class[1:]
				if strings.HasPrefix(body, "-") {
					body = `\-` + body[1:]
				}
				class = "^/" + body
			}
			out.WriteString("[" + byteRunes(class) + "]")
			i = end
		default:
			out.WriteString(regexp.QuoteMeta(byteRunes(pattern[i : i+1])))
		}
	}
	if directory {
		if owners {
			out.WriteString("/.+$")
		} else {
			out.WriteString("/\x00$")
		} // attributes' directory patterns never match files
	} else if owners && literalTail(pattern) {
		out.WriteString("(?:/.*)?$")
	} else {
		out.WriteString("$")
	}
	expression, err := regexp.Compile(out.String())
	if err != nil {
		return nil, err
	}
	return &treeMatcher{expression}, nil
}

// ownerFields retains escaped whitespace in the pattern and strips comments.
// A leading escaped hash is still rejected by treePattern, as GitHub requires.
func ownerFields(line string) []string {
	var fields []string
	var word strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && i+1 < len(line) {
			word.WriteByte(c)
			i++
			word.WriteByte(line[i])
			continue
		}
		if c == '#' {
			break
		}
		if c == ' ' || c == '\t' || c == '\r' {
			if word.Len() > 0 {
				fields = append(fields, word.String())
				word.Reset()
			}
		} else {
			word.WriteByte(c)
		}
	}
	if word.Len() > 0 {
		fields = append(fields, word.String())
	}
	return fields
}

// A literal final component can name a directory (e.g. **/logs). Wildcard
// leaves like docs/* do not extend their match to that directory's contents.
func literalTail(pattern string) bool {
	tail := pattern[strings.LastIndex(pattern, "/")+1:]
	for i := 0; i < len(tail); i++ {
		if tail[i] == '\\' {
			i++
			continue
		}
		if strings.ContainsRune("*?[", rune(tail[i])) {
			return false
		}
	}
	return true
}
