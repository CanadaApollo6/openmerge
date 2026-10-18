package classification

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type attributeEntry struct {
	pattern   *treeMatcher
	generated cp.Truth
}
type attributesFile struct {
	entries     []attributeEntry
	diagnostics []string
}

// Git C-quotes the first field; attribute names/values are whitespace separated.
// Unlike CODEOWNERS, # is only a comment at the beginning of a line.
func attributeFields(line string) ([]string, error) {
	line = strings.Trim(line, " \t\r\n\v\f")
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, nil
	}
	if line[0] != '"' {
		return attributeWords(line), nil
	}
	end := 1
	for ; end < len(line); end++ {
		if line[end] == '\\' {
			if end+1 >= len(line) || !strings.ContainsRune("abtnvfr\\\"01234567", rune(line[end+1])) {
				return nil, fmt.Errorf("unsupported C-quote escape")
			}
			end++
			continue
		}
		if line[end] == '"' {
			break
		}
	}
	if end >= len(line) {
		return nil, fmt.Errorf("unterminated quoted attribute pattern")
	}
	p, err := strconv.Unquote(line[:end+1])
	if err != nil {
		return nil, err
	}
	if end+1 < len(line) && line[end+1] != ' ' && line[end+1] != '\t' {
		return nil, fmt.Errorf("invalid quoted attribute separator")
	}
	return append([]string{p}, attributeWords(line[end+1:])...), nil
}

var attributeName = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*$`)

func attributeWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(" \t\r\n\v\f", r) })
}
func validAttribute(token string) bool {
	if token == "" {
		return false
	}
	if token[0] == '-' || token[0] == '!' {
		token = token[1:]
		if strings.Contains(token, "=") {
			return false
		}
	}
	name, _, _ := strings.Cut(token, "=")
	return attributeName.MatchString(name)
}

func parseAttributes(content string) attributesFile {
	var out attributesFile
	for line, text := range strings.Split(content, "\n") {
		fields, err := attributeFields(text)
		if err == nil && len(fields) == 0 {
			continue
		}
		if err == nil && strings.HasPrefix(fields[0], "[attr]") {
			err = fmt.Errorf("custom attribute macros unsupported")
		}
		var pattern *treeMatcher
		if err == nil {
			pattern, err = treePattern(fields[0], false)
		}
		if err != nil {
			out.diagnostics = append(out.diagnostics, fmt.Sprintf("line %d: %v", line+1, err))
			continue
		}
		for _, attribute := range fields[1:] {
			if !validAttribute(attribute) {
				out.diagnostics = append(out.diagnostics, fmt.Sprintf("line %d: invalid attribute token %q", line+1, attribute))
				continue
			}
			value := cp.False
			switch attribute {
			case "linguist-generated", "linguist-generated=true":
				value = cp.True
			case "-linguist-generated", "!linguist-generated", "linguist-generated=false":
			default:
				if strings.HasPrefix(attribute, "linguist-generated=") {
					out.diagnostics = append(out.diagnostics, fmt.Sprintf("line %d: unsupported generated value %q", line+1, attribute))
				}
				// binary expands to -diff -merge -text, never linguist-generated.
				continue
			}
			out.entries = append(out.entries, attributeEntry{pattern, value})
		}
	}
	return out
}

func generatedAttribute(p string, g forge.GoverningFiles) cp.Truth {
	if !attributesCovered(p, g) {
		return cp.Unknown
	}
	value := cp.False
	for _, name := range attributePaths(p) {
		f, _ := probe(g.Attributes, name)
		if !f.Present {
			continue
		}
		parsed := parseAttributes(f.Content)
		if len(parsed.diagnostics) > 0 {
			return cp.Unknown
		}
		relative := strings.TrimPrefix(p, strings.TrimSuffix(name, ".gitattributes"))
		for _, entry := range parsed.entries {
			if entry.pattern.MatchString(relative) {
				value = entry.generated
			}
		}
	}
	return value
}
