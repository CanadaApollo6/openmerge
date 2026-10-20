package fake

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

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
				mustCleanup(t, f.DeleteBranch(t.Context(), repo, observed.HeadBranch, pr.HeadSHA))
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
				if err := f.DeleteBranch(t.Context(), repo, observed.HeadBranch, pr.HeadSHA); err == nil {
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
