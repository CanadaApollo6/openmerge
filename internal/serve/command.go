package serve

import "strings"

type fence struct {
	marker byte
	length int
	indent int
}

// consume tracks CommonMark fences: up to three spaces, matching markers,
// closing runs at least as long as the opener, and no text after a closer.
func (f *fence) consume(line string) bool {
	if f.indent > 0 {
		if strings.TrimSpace(line) == "" {
			return true
		}
		text, ok := stripIndent(line, f.indent)
		if !ok {
			*f = fence{} // leaving the list also ends its fenced block
		} else {
			line = text
		}
	}
	text := strings.TrimLeft(line, " ")
	if len(line)-len(text) > 3 || text == "" {
		return f.length != 0
	}
	indent := len(line) - len(text)
	if f.length == 0 {
		for {
			content, width := listItem(text)
			if width == 0 {
				break
			}
			text, indent = content, indent+width
		}
	}
	if text == "" {
		return f.length != 0
	}
	marker := text[0]
	if marker != '`' && marker != '~' {
		return f.length != 0
	}
	n := 0
	for n < len(text) && text[n] == marker {
		n++
	}
	if f.length != 0 {
		if marker == f.marker && n >= f.length && strings.Trim(text[n:], " \t\r") == "" {
			*f = fence{}
		}
		return true
	}
	if n >= 3 && (marker != '`' || !strings.Contains(text[n:], "`")) {
		*f = fence{marker: marker, length: n}
		if indent > len(line)-len(strings.TrimLeft(line, " ")) {
			f.indent = indent
		}
		return true
	}
	return false
}

func stripIndent(line string, needed int) (string, bool) {
	column := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			column++
		case '\t':
			column += 4 - column%4
		default:
			return line, false
		}
		if column >= needed {
			return strings.Repeat(" ", column-needed) + line[i+1:], true
		}
	}
	return line, false
}

func listItem(text string) (content string, width int) {
	if text == "" {
		return text, 0
	}
	n := 1
	if !strings.ContainsRune("*-+", rune(text[0])) {
		n = 0
		for n < len(text) && text[n] >= '0' && text[n] <= '9' {
			n++
		}
		if n == 0 || n > 9 || n >= len(text) || text[n] != '.' && text[n] != ')' {
			return text, 0
		}
		n++
	}
	if n >= len(text) || text[n] != ' ' && text[n] != '\t' {
		return text, 0
	}
	end := n
	for end < len(text) && text[end] == ' ' {
		end++
	}
	if end == n || end-n > 4 {
		end = n + 1
	}
	return text[end:], end
}

// blockStart recognizes blocks that interrupt a paragraph's lazy continuation.
func blockStart(text string) bool {
	if text == "" {
		return true
	}
	if text[0] == '#' {
		n := len(text) - len(strings.TrimLeft(text, "#"))
		if n <= 6 && (n == len(text) || text[n] == ' ' || text[n] == '\t') {
			return true
		}
	}
	for _, marker := range []byte{'*', '-', '_'} {
		compact := strings.ReplaceAll(strings.ReplaceAll(text, " ", ""), "\t", "")
		if len(compact) >= 3 && strings.Trim(compact, string(marker)) == "" {
			return true
		}
	}
	if len(text) > 1 && strings.ContainsRune("*-+", rune(text[0])) && (text[1] == ' ' || text[1] == '\t') {
		return strings.TrimSpace(text[2:]) != ""
	}
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	return i == 1 && text[0] == '1' && len(text) > i+1 && (text[i] == '.' || text[i] == ')') && (text[i+1] == ' ' || text[i+1] == '\t') && strings.TrimSpace(text[i+2:]) != ""
}

func command(body string) string {
	var code, quotedCode fence
	quoteParagraph := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if code.consume(line) {
			quoteParagraph = false
			continue
		}
		text := strings.TrimLeft(line, " ")
		indented := len(line)-len(text) > 3 || strings.HasPrefix(text, "\t")
		quoted := text
		for {
			content, width := listItem(quoted)
			if width == 0 {
				break
			}
			quoted = content
		}
		if !indented && strings.HasPrefix(quoted, ">") {
			quotedIndented := false
			for {
				if strings.HasPrefix(quoted, ">") {
					quoted = strings.TrimPrefix(quoted, ">")
					quoted = strings.TrimPrefix(strings.TrimPrefix(quoted, " "), "\t")
					trimmed := strings.TrimLeft(quoted, " ")
					quotedIndented = len(quoted)-len(trimmed) > 3 || strings.HasPrefix(trimmed, "\t")
					if quotedIndented {
						break
					}
					quoted = trimmed
					continue
				}
				content, width := listItem(quoted)
				if width == 0 {
					break
				}
				quoted = content
			}
			inCode := quotedCode.consume(quoted)
			quoteParagraph = !inCode && (!quotedIndented || quoteParagraph) && !blockStart(quoted)
			continue
		}
		quotedCode = fence{}
		if strings.TrimSpace(text) == "" || !indented && blockStart(text) {
			quoteParagraph = false
		}
		if indented || quoteParagraph {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 || fields[0] != "@openmerge" {
			continue
		}
		switch fields[1] {
		case "queue":
			if len(fields) == 2 || len(fields) == 3 {
				return strings.Join(fields[1:], " ")
			}
		case "dequeue", "refresh":
			if len(fields) == 2 {
				return fields[1]
			}
		}
	}
	return ""
}

func shaPrefix(value string) bool {
	if len(value) < 7 || len(value) > 40 {
		return false
	}
	for _, c := range strings.ToLower(value) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
