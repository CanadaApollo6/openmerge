package condition

import "fmt"

// Suggestion returns an optional diagnostic suffix using the shared spelling policy.
func Suggestion(input string, candidates []string) string {
	if closest := Closest(input, candidates); closest != "" {
		return fmt.Sprintf("; did you mean %q?", closest)
	}
	return ""
}

// Closest returns the nearest candidate within max(1, rune count / 3) edits,
// or an empty string. An adjacent transposition counts as one edit.
// Ties use candidate order, shared by condition and schema diagnostics.
func Closest(input string, candidates []string) string {
	best, distance := "", int(^uint(0)>>1)
	a := []rune(input)
	for _, candidate := range candidates {
		b := []rune(candidate)
		row := make([]int, len(b)+1)
		for j := range row {
			row[j] = j
		}
		var previousRow []int
		for i, x := range a {
			nextRow := make([]int, len(b)+1)
			nextRow[0] = i + 1
			for j, y := range b {
				cost := 0
				if x != y {
					cost = 1
				}
				nextRow[j+1] = min(nextRow[j]+1, row[j+1]+1, row[j]+cost)
				if i > 0 && j > 0 && x == b[j-1] && a[i-1] == y {
					nextRow[j+1] = min(nextRow[j+1], previousRow[j-1]+1)
				}
			}
			previousRow, row = row, nextRow
		}
		if row[len(b)] < distance {
			best, distance = candidate, row[len(b)]
		}
	}
	if distance > max(1, len(a)/3) {
		return ""
	}
	return best
}
