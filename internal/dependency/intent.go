package dependency

import (
	"fmt"
	"slices"
	"strings"
)

func sortReferences(refs []Reference) {
	slices.SortFunc(refs, func(a, b Reference) int { return strings.Compare(a.ChangeID, b.ChangeID) })
}

// ParseIntent reads the authoritative PR body, including a trailer-shaped block
// in that body. It never scans commits or comments. Fences, indented code, block
// quotes and HTML comments carry no declarations. No partial result is returned
// on a malformed recognized line. CRLF and up to three leading spaces are allowed.
func ParseIntent(body string, scope Scope) ([]Input, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var out []Input
	var fence byte
	var fenceLength int
	inComment := false
	for i, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if fence != 0 {
			if indent <= 3 && fenceRun(trimmed, fence) >= fenceLength && strings.TrimSpace(trimmed[fenceRun(trimmed, fence):]) == "" {
				fence = 0
			}
			continue
		}
		// An indented/quoted example cannot open a comment or a fence in body prose.
		if !inComment && (indent >= 4 || strings.HasPrefix(trimmed, "\t") || strings.HasPrefix(trimmed, ">")) {
			continue
		}
		// A fence's info string is literal code metadata, so comment-looking text
		// on its opening line must not leak comment state into following prose.
		if !inComment {
			if marker, count := openingFence(trimmed); marker != 0 {
				fence, fenceLength = marker, count
				continue
			}
		}
		line = uncomment(line, &inComment)
		trimmed = strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) >= 4 || strings.HasPrefix(trimmed, "\t") || strings.HasPrefix(trimmed, ">") {
			continue
		}
		if marker, count := openingFence(trimmed); marker != 0 {
			fence, fenceLength = marker, count
			continue
		}
		value, recognized := strings.CutPrefix(trimmed, "Depends-On:")
		if !recognized {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 1 {
			return nil, fmt.Errorf("line %d: %w", i+1, ErrMalformed)
		}
		input, err := parseInput(fields[0], scope)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, input)
	}
	return out, nil
}

func fenceRun(line string, marker byte) int {
	n := 0
	for n < len(line) && line[n] == marker {
		n++
	}
	return n
}

func uncomment(line string, inside *bool) string {
	var out strings.Builder
	for len(line) != 0 {
		if *inside {
			_, rest, end := strings.Cut(line, "-->")
			if !end {
				return out.String()
			}
			*inside, line = false, rest
			out.WriteByte(' ')
		} else {
			before, rest, start := strings.Cut(line, "<!--")
			out.WriteString(before)
			if !start {
				break
			}
			*inside, line = true, rest
			out.WriteByte(' ')
		}
	}
	return out.String()
}

func openingFence(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	count := fenceRun(line, line[0])
	if count < 3 || (line[0] == '`' && strings.Contains(line[count:], "`")) {
		return 0, 0
	}
	return line[0], count
}
