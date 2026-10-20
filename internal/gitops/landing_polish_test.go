package gitops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLinkedCommitAuthorsExcludePrimaryAliases(t *testing.T) {
	for _, approval := range []bool{false, true} {
		for _, scenario := range []string{"id", "login", "unlinked", "different-id", "unicode-long-s", "unicode-kelvin", "unicode-accent", "read-error"} {
			t.Run(fmt.Sprintf("approval=%v/%s", approval, scenario), func(t *testing.T) {
				f := newFixture(t)
				authored := func(parent, name, email, file, trailers string) string {
					sha := f.commit(parent, "change", map[string]string{file: file})
					return gitCommand(t, f.source, "change\n\n"+trailers+"\n", []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email}, "commit-tree", sha+"^{tree}", "-p", parent)
				}
				one := authored(f.base, "Riel", "riel@example.test", "one", "Signed-off-by: Riel <riel@example.test>\nCo-authored-by: Alias <alias@example.test>\nCo-authored-by: Carol <carol@example.test>")
				two := authored(one, "Alias", "alias@example.test", "two", "Signed-off-by: Alias <alias@example.test>")
				three := authored(two, "Bob", "bob@example.test", "three", "Co-authored-by: Bob Duplicate <BOB@example.test>")
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, three+":"+headRef(1))
				f.prs[0].HeadSHA = three
				f.g.cfg.PullRequest = func(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
					author := "login"
					if scenario == "unicode-long-s" {
						author = "sam"
					}
					if scenario == "unicode-accent" {
						author = "Élodie"
					}
					if scenario == "unicode-kelvin" {
						author = "Kontributor"
					}
					return forge.PullRequest{Number: 1, HeadSHA: three, Author: author, AuthorID: 12, AuthorEmail: "12+login@users.noreply.github.com", Title: "Polish", HeadBranch: "topic"}, nil
				}
				calls := 0
				f.g.cfg.CommitAuthors = func(context.Context, forge.RepoRef, int) ([]forge.CommitAuthor, error) {
					calls++
					id, login := int64(12), "renamed-login"
					switch scenario {
					case "login":
						id, login = 0, "LOGIN"
					case "unicode-long-s":
						id, login = 0, "ſam"
					case "unicode-kelvin":
						id, login = 0, "Kontributor"
					case "unicode-accent":
						id, login = 0, "élodie"
					case "unlinked":
						id, login = 0, ""
					case "different-id":
						id, login = 99, "login"
					case "read-error":
						return nil, errors.New("API unavailable")
					}
					return []forge.CommitAuthor{{SHA: one, ID: id, Login: login}, {SHA: two, ID: id, Login: login}}, nil
				}
				if approval {
					f.prs[0].Metadata = &forge.ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1}
				}
				tested, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "test", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Squash, BatchID: "linked"}, forge.PushOptions{})
				if calls != 1 {
					t.Fatalf("commit author reads=%d, want one per PR build", calls)
				}
				if scenario == "read-error" {
					if err == nil {
						t.Fatal("build ignored author API failure")
					}
					return
				}
				must(t, err)
				f.fetch(tested)
				message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", tested)
				primary := scenario == "id" || scenario == "login" || !approval && (scenario == "unicode-long-s" || scenario == "unicode-kelvin" || scenario == "unicode-accent")
				for _, value := range []string{"Co-authored-by: Riel <riel@example.test>", "Co-authored-by: Alias <alias@example.test>"} {
					if strings.Contains(message, value) == primary {
						t.Fatalf("incorrect primary attribution: %s", message)
					}
				}
				for _, value := range []string{"Signed-off-by: Riel <riel@example.test>", "Signed-off-by: Alias <alias@example.test>", "Co-authored-by: Carol <carol@example.test>", "Co-authored-by: Bob <bob@example.test>"} {
					if strings.Count(message, value) != 1 {
						t.Fatalf("lost or duplicate attribution: %s", message)
					}
				}
				if strings.Contains(message, "Bob Duplicate") {
					t.Fatal("email dedupe lost")
				}
			})
		}
	}
}

func TestDeleteBranchLeasesExactHead(t *testing.T) {
	f := newFixture(t)
	f.push("topic/nested", f.heads[1], false)
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "topic/nested", f.heads[2], forge.PushOptions{}))
	if f.read("topic/nested") != f.heads[1] {
		t.Fatal("cleanup deleted a different head")
	}
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "topic/nested", f.heads[1], forge.PushOptions{}))
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "topic/nested", f.heads[1], forge.PushOptions{}))
	if _, err := f.g.ReadRef(t.Context(), testRepo, "topic/nested"); err == nil {
		t.Fatal("cleanup retained head")
	}
	for _, branch := range []string{"", "refs/heads/topic", "openmerge/fence/main", "openmerge/queue/main/b1", "../main"} {
		if err := f.g.DeleteBranch(t.Context(), testRepo, branch, f.base, forge.PushOptions{}); err == nil {
			t.Fatalf("accepted unsafe branch %q", branch)
		}
	}
}

func TestDeleteBranchPreservesConcurrentPush(t *testing.T) {
	f := newFixture(t)
	f.push("topic", f.heads[1], false)
	resolve := f.g.cfg.Remote
	resolves := 0
	f.g.cfg.Remote = func(ctx context.Context, repo forge.RepoRef) (string, Auth, error) {
		resolves++
		if resolves == 2 {
			// Replace the branch after ls-remote, before the leased deletion.
			f.push("topic", f.heads[2], true)
		}
		return resolve(ctx, repo)
	}
	must(t, f.g.DeleteBranch(t.Context(), testRepo, "topic", f.heads[1], forge.PushOptions{}))
	if f.read("topic") != f.heads[2] {
		t.Fatal("cleanup removed concurrent work")
	}
}
