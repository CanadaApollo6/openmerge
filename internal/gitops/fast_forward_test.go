package gitops

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestFastForwardPreservesHeadsAndAtomicLeases(t *testing.T) {
	for _, scenario := range []string{"singleton", "stack", "siblings", "reverse", "equal", "stale-head", "moved-base", "moved-fence"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			first := f.heads[1]
			second := f.commit(first, "Author's message\n\nSigned-off-by: Original <original@example.test>", map[string]string{"two": "two"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, second+":"+headRef(2))
			prs := []forge.BuildPR{{Number: 1, HeadSHA: first}, {Number: 2, HeadSHA: second}}
			if scenario == "singleton" {
				prs = prs[:1]
			}
			switch scenario {
			case "siblings":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, f.heads[2]+":"+headRef(2))
				prs[1].HeadSHA = f.heads[2]
			case "reverse":
				prs[0], prs[1] = prs[1], prs[0]
			case "equal":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, first+":"+headRef(2))
				prs[1].HeadSHA = first
			case "stale-head":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, f.heads[2]+":"+headRef(2))
			}
			f.g.cfg.PullRequest = func(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
				t.Fatal("fast-forward read attribution metadata")
				return forge.PullRequest{}, nil
			}
			before := gitCommand(t, f.source, "", nil, "cat-file", "commit", prs[len(prs)-1].HeadSHA)
			ref := "openmerge/queue/main/ff"
			tested, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: prs, Method: forge.FastForward, BatchID: "ff"})
			if scenario == "siblings" || scenario == "reverse" || scenario == "equal" || scenario == "stale-head" {
				if err == nil {
					t.Fatal("invalid fast-forward accepted")
				}
				if scenario != "stale-head" && !errors.Is(err, forge.ErrNotFastForward) {
					t.Fatal(err)
				}
				if _, err := f.g.ReadRef(t.Context(), testRepo, ref); err == nil {
					t.Fatal("failed build published a test ref")
				}
				return
			}
			must(t, err)
			if tested != prs[len(prs)-1].HeadSHA || f.read(ref) != tested {
				t.Fatal("fast-forward created a new commit")
			}
			f.fetch(tested)
			if got := gitCommand(t, f.source, "", nil, "cat-file", "commit", tested); got != before {
				t.Fatal("commit bytes changed")
			}
			fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
			must(t, err)
			switch scenario {
			case "moved-base":
				f.push("main", f.heads[3], false)
			case "moved-fence":
				_, err = f.g.AdvanceFence(t.Context(), testRepo, testFence, fence, "abandon/other")
				must(t, err)
			}
			baseBefore, fenceBefore := f.read("main"), f.read(testFence)
			_, err = f.g.Land(t.Context(), testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/ff", PRs: prs})
			if scenario == "moved-base" || scenario == "moved-fence" {
				if !errors.Is(err, forge.ErrLeaseRejected) {
					t.Fatal(err)
				}
				if f.read("main") != baseBefore || f.read(testFence) != fenceBefore {
					t.Fatal("rejection moved refs")
				}
				if scenario == "moved-base" {
					_, err = f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: baseBefore, PRs: prs, Method: forge.FastForward, BatchID: "retry"})
					if !errors.Is(err, forge.ErrNotFastForward) {
						t.Fatalf("diverged head was rebased: %v", err)
					}
				}
				return
			}
			must(t, err)
			if f.read("main") != tested || f.read(testFence) == fence {
				t.Fatal("landing failed to atomically update both refs")
			}
		})
	}
}
