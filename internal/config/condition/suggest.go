package condition

// Closest returns the candidate with the smallest Levenshtein edit distance.
// Ties use candidate order. The schema loader shares this spelling policy.
func Closest(input string, candidates []string) string {
	best, distance := "", int(^uint(0)>>1)
	a := []rune(input)
	for _, candidate := range candidates {
		b := []rune(candidate)
		row := make([]int, len(b)+1)
		for j := range row {
			row[j] = j
		}
		for i, x := range a {
			previous := row[0]
			row[0] = i + 1
			for j, y := range b {
				cost := 0
				if x != y {
					cost = 1
				}
				next := min(row[j]+1, row[j+1]+1, previous+cost)
				previous, row[j+1] = row[j+1], next
			}
		}
		if row[len(b)] < distance {
			best, distance = candidate, row[len(b)]
		}
	}
	return best
}
