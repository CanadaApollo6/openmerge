package fake

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestStaleAdvanceFenceDoesNotMintCommit(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "owner/repo"}
	_, initial := f.Init(repo, "main", "openmerge/fence/main")
	next, err := f.AdvanceFence(t.Context(), repo, "openmerge/fence/main", initial, "abandon/first", forge.PushOptions{})
	mustCleanup(t, err)
	before := len(f.repo(repo).commits)
	_, err = f.AdvanceFence(t.Context(), repo, "openmerge/fence/main", initial, "abandon/stale", forge.PushOptions{})
	if !errors.Is(err, forge.ErrLeaseRejected) {
		t.Fatalf("stale advance = %v, want ErrLeaseRejected", err)
	}
	if after := len(f.repo(repo).commits); after != before {
		t.Fatalf("stale advance minted commits: %d -> %d", before, after)
	}
	got, err := f.ReadRef(t.Context(), repo, "openmerge/fence/main")
	mustCleanup(t, err)
	if got != next {
		t.Fatal("stale advance moved the fence")
	}
}

func mustCleanup(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCleanupKeepsDefaultMissingMovedAndQualifiedHeads(t *testing.T) {
	for _, scenario := range []string{"default", "missing", "moved", "qualified", "protected"} {
		t.Run(scenario, func(t *testing.T) {
			f := New()
			repo := forge.RepoRef{Path: "owner/repo"}
			base, _ := f.Init(repo, "main", "openmerge/fence/main")
			observed, err := f.OpenPR(repo, 1, base, true)
			mustCleanup(t, err)
			pr := forge.BuildPR{Number: 1, HeadSHA: observed.HeadSHA}
			mustCleanup(t, f.ClosePR(repo, pr.Number))
			switch scenario {
			case "default":
				f.SetDefaultBranch(repo, observed.HeadBranch)
			case "missing":
				mustCleanup(t, f.DeleteBranch(t.Context(), repo, observed.HeadBranch, pr.HeadSHA, forge.PushOptions{}))
			case "moved":
				_, err = f.PushBase(repo, observed.HeadBranch, "move", nil)
				mustCleanup(t, err)
			case "qualified":
				f.mu.Lock()
				r := f.repo(repo)
				q := r.prs[pr.Number]
				q.HeadBranch = "refs/heads/" + q.HeadBranch
				r.prs[pr.Number] = q
				f.mu.Unlock()
			case "protected":
				f.SetBranchProtected(repo, observed.HeadBranch, true)
			}
			branch, err := f.HeadBranchForDeletion(t.Context(), repo, pr.Number, pr.HeadSHA, observed.Base)
			mustCleanup(t, err)
			if branch != "" {
				t.Fatalf("unsafe cleanup returned %q", branch)
			}
			if scenario == "protected" {
				if err := f.DeleteBranch(t.Context(), repo, observed.HeadBranch, pr.HeadSHA, forge.PushOptions{}); err == nil {
					t.Fatal("protected deletion reported success")
				}
				tip, err := f.ReadRef(t.Context(), repo, observed.HeadBranch)
				mustCleanup(t, err)
				if tip != pr.HeadSHA {
					t.Fatal("protected branch changed")
				}
			}
		})
	}
}
