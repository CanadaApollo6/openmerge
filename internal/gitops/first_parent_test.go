package gitops

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestFirstParentCommitsSelectsGeneratedMergeHistoryCacheOnly(t *testing.T) {
	f := newFixture(t)
	tested := f.build(forge.Merge, f.prs[:2], "first-parent")
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		t.Error("tested history resolved a remote")
		return "", Auth{}, errors.New("remote unavailable")
	}
	chain, truncated, err := f.g.FirstParentCommits(t.Context(), testRepo, f.base, tested, 2)
	must(t, err)
	if truncated || len(chain) != 2 || chain[0] != tested {
		t.Fatalf("generated chain: %v truncated=%v", chain, truncated)
	}
	for _, pr := range f.prs[:2] {
		if slices.Contains(chain, pr.HeadSHA) {
			t.Fatal("contributor parent selected")
		}
	}
	capped, truncated, err := f.g.FirstParentCommits(t.Context(), testRepo, f.base, tested, 1)
	must(t, err)
	if !truncated || !slices.Equal(capped, chain[:1]) {
		t.Fatalf("history cap lost: %v %v", capped, truncated)
	}
	for _, base := range []string{f.prs[0].HeadSHA, strings.Repeat("f", 40), "main"} {
		if _, _, err := f.g.FirstParentCommits(t.Context(), testRepo, base, tested, 10); err == nil {
			t.Fatal("accepted unavailable/non-first-parent base", base)
		}
	}
}
