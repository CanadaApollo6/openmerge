package forge

import "strings"

// ParseBatchMessage reads a generated commit's authoritative batch trailer.
// It fails closed unless the final paragraph is a trailer block and the message
// contains exactly one OpenMerge-Batch key, with a nonempty, single-line value.
func ParseBatchMessage(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	if strings.ContainsAny(message, "\x00\r") {
		return ""
	}
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	start, occurrences := -1, 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			start = i + 1
		}
		key, _, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "OpenMerge-Batch") {
			occurrences++
		}
	}
	if start <= 0 || occurrences != 1 {
		return ""
	}
	batch, previous := "", ""
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if previous == "" || strings.EqualFold(previous, "OpenMerge-Batch") {
				return ""
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimRight(key, " \t")
		if !ok || key == "" {
			return ""
		}
		for _, c := range key {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return ""
			}
		}
		if strings.EqualFold(key, "OpenMerge-Batch") {
			batch = strings.TrimSpace(value)
		}
		previous = key
	}
	return batch
}
