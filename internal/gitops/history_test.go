package gitops

import (
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCommitsBetweenGraphRewriteUnknownAndCap(t *testing.T) {
	f := newFixture(t)
	head := f.base
	var commits []string
	for i := range 4 {
		head = f.commit(head, strings.Repeat("admin", i+1), nil)
		commits = append(commits, head)
	}
	f.push("main", head, false)
	got, truncated, err := f.g.CommitsBetween(t.Context(), testRepo, f.base, head, 3)
	must(t, err)
	slices.Reverse(commits)
	if !truncated || !slices.Equal(got, commits[:3]) {
		t.Fatalf("capped history: %v %t", got, truncated)
	}
	got, truncated, err = f.g.CommitsBetween(t.Context(), testRepo, f.base, head, 4)
	must(t, err)
	if truncated || !slices.Equal(got, commits) {
		t.Fatal("exact cap falsely truncated")
	}
	// Rollback is a rewrite with a known empty introduced range.
	got, truncated, err = f.g.CommitsBetween(t.Context(), testRepo, head, f.base, 4)
	must(t, err)
	if truncated || len(got) != 0 {
		t.Fatal("rollback invented commits")
	}
	// All merged side histories must appear, not just the first-parent chain.
	merged := f.build(forge.Merge, f.prs[:2], "outside-merge")
	got, _, err = f.g.CommitsBetween(t.Context(), testRepo, f.base, merged, 100)
	must(t, err)
	for _, pr := range f.prs[:2] {
		if !slices.Contains(got, pr.HeadSHA) {
			t.Fatal("side history omitted")
		}
	}
	_, _, err = f.g.CommitsBetween(t.Context(), testRepo, strings.Repeat("f", 40), head, 100)
	if err == nil {
		t.Fatal("unreadable history claimed known")
	}
}
