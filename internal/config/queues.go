package config

import (
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
)

// MatchesBase resolves an omitted selector to the repository's default branch.
// Explicit selectors use the same slash-aware glob grammar as conditions.
func (q Queue) MatchesBase(branch, defaultBranch string) bool {
	if q.Base == "" {
		return branch == defaultBranch
	}
	pattern, err := condition.CompileGlob(q.Base)
	return err == nil && pattern.MatchString(branch)
}

// ResolveBranch selects policy for one concrete branch. Overlap diagnostics name
// only the matching queues; callers can still resolve other branches from c.
// Branch-dependent validation is separate from Load, which needs no forge reads.
func (c Config) ResolveBranch(branch, defaultBranch string) (Queue, Errors) {
	var indices []int
	var names []string
	for i, q := range c.Queues {
		if q.MatchesBase(branch, defaultBranch) {
			indices = append(indices, i)
			names = append(names, q.Name)
		}
	}
	switch len(indices) {
	case 0:
		return Queue{}, Errors{{Line: 1, Path: "queues", Message: fmt.Sprintf("no queue matches base branch %q", branch)}}
	case 1:
		return c.Queues[indices[0]], nil
	default:
		var diagnostics Errors
		for _, i := range indices {
			diagnostics = append(diagnostics, Diagnostic{
				Line: max(1, c.Queues[i].baseLine), Path: fmt.Sprintf("queues[%d].base", i),
				Message: fmt.Sprintf("queue %q overlaps on branch %q with queues: %s", c.Queues[i].Name, branch, strings.Join(names, ", ")),
			})
		}
		return Queue{}, diagnostics
	}
}
