package classification

import (
	"fmt"
	"unicode/utf8"
)

// Keep the first eight source diagnostics and an explicit omitted count.
func boundedDiagnostics(values []string) []string {
	for i := range values {
		if len(values[i]) > 1024 {
			cut := 1008
			for !utf8.ValidString(values[i][:cut]) {
				cut--
			}
			values[i] = values[i][:cut] + "...[truncated]"
		}
	}
	if len(values) <= 8 {
		return values
	}
	return append(values[:8:8], fmt.Sprintf("+%d more", len(values)-8))
}
