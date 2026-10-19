package serve

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type stageAHistoryProbe struct {
	forge.Git
	sources          []int
	sourceLimits     []int
	firstParentLimit int
	truncate         bool
}

func (p *stageAHistoryProbe) CommitsBetween(_ context.Context, _ forge.RepoRef, _, _ string, limit int) ([]string, bool, error) {
	n := p.sources[len(p.sourceLimits)]
	p.sourceLimits = append(p.sourceLimits, limit)
	return make([]string, min(n, limit)), n > limit, nil
}
func (p *stageAHistoryProbe) FirstParentCommits(_ context.Context, _ forge.RepoRef, _, _ string, limit int) ([]string, bool, error) {
	p.firstParentLimit = limit
	commits := make([]string, limit)
	for i := range commits {
		commits[i] = fmt.Sprint(limit - i)
	}
	return commits, p.truncate, nil
}

func TestStageATestedHistoryLimits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		method    queue.MergeMethod
		members   int
		sources   []int
		truncated bool
		wantLimit int
		wantError string
	}{
		{name: "merge-depth-four", method: queue.Merge, members: 4, wantLimit: 4},
		{name: "squash-depth-four", method: queue.Squash, members: 4, wantLimit: 4},
		{name: "rebase-depth-four", method: queue.Rebase, members: 4, sources: []int{2, 3, 1, 2}, wantLimit: 8},
		{name: "merge-truncated", method: queue.Merge, members: 4, truncated: true, wantLimit: 4, wantError: "tested member history exceeds composition limit 4"},
		{name: "rebase-truncated", method: queue.Rebase, members: 1, sources: []int{2}, truncated: true, wantLimit: 2, wantError: "tested member history exceeds composition limit 2"},
		{name: "empty", method: queue.Merge, wantError: "tested member count 0"},
		{name: "too-many-members", method: queue.Merge, members: forge.MaxFirstParentCommits + 1, wantError: "tested member count"},
		{name: "source-over-limit", method: queue.Rebase, members: 1, sources: []int{forge.MaxCommitHistoryCommits + 1}, wantError: "tested source history for PR #1 exceeds remaining composition limit"},
		{name: "aggregate-over-limit", method: queue.Rebase, members: 2, sources: []int{forge.MaxFirstParentCommits - 1, 2}, wantError: "tested source history for PR #2 exceeds remaining composition limit 1"},
		{name: "aggregate-full-before-last-member", method: queue.Rebase, members: 2, sources: []int{forge.MaxFirstParentCommits, 1}, wantError: "tested source history exceeds first-parent limit"},
		{name: "exact-maximum", method: queue.Rebase, members: 1, sources: []int{forge.MaxFirstParentCommits}, wantLimit: forge.MaxFirstParentCommits},
		{name: "source-empty", method: queue.Rebase, members: 1, sources: []int{0}, wantError: "unexpected rebase tested member structure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &stageAHistoryProbe{sources: tc.sources, truncate: tc.truncated}
			w := &worker{Target: Target{Git: probe}}
			pins := make([]queue.BatchPR, tc.members)
			for i := range pins {
				pins[i].PR = i + 1
			}
			members, err := testedMemberCommits(t.Context(), w, queue.Land{Method: tc.method, PRs: pins}, probe)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || len(members) != 0 {
					t.Fatalf("members=%v err=%v, want %q", members, err, tc.wantError)
				}
			} else {
				must(t, err)
				if len(members) != tc.members {
					t.Fatalf("selected %d members, want %d", len(members), tc.members)
				}
				if tc.name == "rebase-depth-four" && strings.Join(members, ",") != "2,5,6,8" {
					t.Fatalf("rebase segment markers %v", members)
				}
			}
			if probe.firstParentLimit != tc.wantLimit {
				t.Fatalf("first-parent limit=%d, want %d", probe.firstParentLimit, tc.wantLimit)
			}
			for _, limit := range probe.sourceLimits {
				if limit < 1 || limit > forge.MaxCommitHistoryCommits {
					t.Fatalf("invalid source limit %d", limit)
				}
			}
		})
	}
}
