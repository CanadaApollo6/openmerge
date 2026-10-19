// Package ascii provides case folding without Unicode identity aliases.
package ascii

import "strings"

// Lower folds only ASCII A-Z, preserving every other byte.
func Lower(s string) string {
	if !strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// EqualFold compares using only ASCII case folding.
func EqualFold(a, b string) bool { return Lower(a) == Lower(b) }
