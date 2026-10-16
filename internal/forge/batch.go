package forge

import "strings"

// ValidBatchID accepts the ASCII identifier alphabet used in generated messages.
func ValidBatchID(id string) bool {
	if id == "" {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// ParseBatchMessage reads OpenMerge's own final-line format, independently of
// Git's trailer grammar. Any other line starting with the batch key is invalid.
func ParseBatchMessage(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	if strings.ContainsAny(message, "\x00\r") {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(message, "\n"), "\n")
	for i, line := range lines {
		if i < len(lines)-1 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "openmerge-batch") {
			return ""
		}
	}
	id, ok := strings.CutPrefix(lines[len(lines)-1], "OpenMerge-Batch: ")
	if !ok || !ValidBatchID(id) {
		return ""
	}
	return id
}
