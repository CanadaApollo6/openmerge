package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// MatchesBase resolves an omitted selector to the repository's default branch.
// Explicit selectors use the same slash-aware glob grammar as conditions.
func (q Queue) MatchesBase(branch, defaultBranch string) bool {
	if q.Base == "" {
		return branch == defaultBranch
	}
	pattern := q.basePattern
	if pattern == nil || q.Base != q.basePatternSource {
		// Queue literals and edited selectors still use the loaded grammar.
		var err error
		pattern, err = condition.CompileGlob(q.Base)
		if err != nil {
			return false
		}
	}
	return pattern.MatchString(branch)
}

// Digest identifies the effective policy for a resolved queue, independently of
// YAML formatting, selectors, other queues, and the default branch's commit.
func (q Queue) Digest(deleteHeadBranch bool) string {
	policy := struct {
		EnterWhen, MergeWhen string
		Planner              queue.Config
		DeleteHeadBranch     bool
	}{q.EnterWhen.String(), q.MergeWhen.String(), q.QueueConfig(), deleteHeadBranch}
	data, _ := json.Marshal(policy)
	return fmt.Sprintf("%x", sha256.Sum256(data))
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
