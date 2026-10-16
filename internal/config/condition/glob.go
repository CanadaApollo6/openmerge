package condition

import (
	"fmt"
	"regexp"
	"strings"
)

// CompileGlob compiles a whole-path glob for conditions and queue base selectors.
func CompileGlob(pattern string) (*regexp.Regexp, error) {
	expression, err := globPattern(pattern)
	if err != nil {
		return nil, err
	}
	return regexp.Compile(expression)
}

// Globs match a whole slash-separated path: * and ? stay within a segment,
// ** crosses slashes, and **/ also matches zero directories. A backslash quotes
// the next character. Other characters (including brackets) are literal.
func globPattern(pattern string) (string, error) {
	runes := []rune(pattern)
	var out strings.Builder
	out.WriteString(`\A(?s:`)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				if i+1 < len(runes) && runes[i+1] == '/' {
					i++
					out.WriteString(`(?:.*/)?`)
				} else {
					out.WriteString(`.*`)
				}
			} else {
				out.WriteString(`[^/]*`)
			}
		case '?':
			out.WriteString(`[^/]`)
		case '\\':
			i++
			if i == len(runes) {
				return "", fmt.Errorf("trailing backslash")
			}
			out.WriteString(regexp.QuoteMeta(string(runes[i])))
		default:
			out.WriteString(regexp.QuoteMeta(string(runes[i])))
		}
	}
	out.WriteString(`)\z`)
	return out.String(), nil
}
