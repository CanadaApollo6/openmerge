package gitops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLandingAttribution(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			date := epoch.Add(-24 * time.Hour).Format(time.RFC3339)
			authored := func(parent, message, name, email, file string) string {
				provisional := f.commit(parent, message, map[string]string{file: file})
				return gitCommand(t, f.source, message+"\n", []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date}, "commit-tree", provisional+"^{tree}", "-p", parent)
			}
			one := authored(f.base, "first\n\nSigned-off-by: Alice <alice@example.test>", "alice", "12+alice@users.noreply.github.com", "one")
			two := authored(one, "second\n\nCo-authored-by: Carol <carol@example.test>\nSigned-off-by: Bob <bob@example.test>\nSigned-off-by: Alice <alice@example.test>", "Bob", "bob@example.test", "two")
			three := authored(two, "third\n\nSigned-off-by: Bob <bob@example.test>", "Bob", "bob@example.test", "three")
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, three+":"+headRef(1))
			f.prs[0].HeadSHA = three
			metadata := map[int]forge.PullRequest{
				1: {Number: 1, HeadSHA: three, Author: "alice", AuthorID: 12, AuthorEmail: "12+alice@users.noreply.github.com", Title: "Fix the queue", Body: " \nDescription\n\n<!-- keep this -->\n<!-- openmerge:key=status -->\n ", HeadBranch: "alice:fix"},
				2: {Number: 2, HeadSHA: f.heads[2], Author: "dan", AuthorID: 14, AuthorEmail: "14+dan@users.noreply.github.com", Title: "Update docs", Body: "Second PR body", HeadBranch: "dan:docs"},
			}
			f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) { return metadata[n], nil }
			f.g.cfg.Committer = Identity{Name: "queue-app[bot]", Email: "99+queue-app[bot]@users.noreply.github.com"}
			tested := f.build(method, f.prs[:2], "attribution")
			f.fetch(tested)
			history := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--first-parent", "--reverse", f.base+".."+tested))
			if method == forge.Rebase {
				for i, original := range []string{one, two, three, f.heads[2]} {
					want := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an%x00%ae%x00%aI", original)
					got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an%x00%ae%x00%aI", history[i])
					if got != want {
						t.Fatalf("replayed author = %q, want %q", got, want)
					}
					originalTrailers := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=Signed-off-by)", original)
					if got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=Signed-off-by)", history[i]); got != originalTrailers {
						t.Fatalf("replay lost sign-offs: %q != %q", got, originalTrailers)
					}
				}
			} else {
				for i, n := range []int{1, 2} {
					pr := metadata[n]
					got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an <%ae>|%cn <%ce>", history[i])
					want := fmt.Sprintf("%s <%s>|queue-app[bot] <99+queue-app[bot]@users.noreply.github.com>", pr.Author, pr.AuthorEmail)
					if got != want {
						t.Fatalf("commit identity = %q, want %q", got, want)
					}
					message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", history[i])
					subject := fmt.Sprintf("%s (#%d)", pr.Title, n)
					if method == forge.Merge {
						subject = fmt.Sprintf("Merge pull request #%d from %s\n\n%s", n, pr.HeadBranch, pr.Title)
					}
					if !strings.HasPrefix(message, subject) || strings.Contains(message, "<!-- openmerge:") {
						t.Fatalf("bad message: %q", message)
					}
					if method == forge.Squash && !strings.Contains(message, strings.TrimSpace(openmergeMarker.ReplaceAllString(pr.Body, ""))) {
						t.Fatalf("PR body missing: %q", message)
					}
					if n == 1 {
						for _, trailer := range []string{"Co-authored-by: Bob <bob@example.test>", "Co-authored-by: Carol <carol@example.test>", "Signed-off-by: Alice <alice@example.test>", "Signed-off-by: Bob <bob@example.test>"} {
							if strings.Count(message, trailer) != 1 {
								t.Fatalf("trailer missing or duplicated %q: %q", trailer, message)
							}
						}
						if strings.Contains(message, "Co-authored-by: alice") {
							t.Fatal("primary author also credited as coauthor")
						}
					}
				}
			}
			for i, sha := range history {
				trailers := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=OpenMerge-Batch,valueonly)", sha)
				want := ""
				if i == len(history)-1 {
					want = "attribution"
				}
				if trailers != want {
					t.Fatalf("batch trailer on commit %d = %q, want %q", i, trailers, want)
				}
			}
			before := gitCommand(t, f.source, "", nil, "cat-file", "commit", tested)
			pr := metadata[1]
			pr.Title, pr.Body = "Edited after testing", "new body"
			metadata[1] = pr
			fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
			must(t, err)
			_, err = f.g.Land(t.Context(), testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/attribution", PRs: f.prs[:2]})
			must(t, err)
			if tip := f.read("main"); tip != tested || gitCommand(t, f.remote, "", nil, "cat-file", "commit", tip) != before {
				t.Fatal("landing changed tested commit bytes")
			}
		})
	}
}

func TestBuildRefusesStaleOrInvalidMetadata(t *testing.T) {
	for _, invalid := range []string{"head", "author", "title", "error"} {
		t.Run(invalid, func(t *testing.T) {
			f := newFixture(t)
			f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
				pr := forge.PullRequest{Number: n, HeadSHA: f.heads[n], Author: "alice", AuthorEmail: "12+alice@users.noreply.github.com", Title: "Title", HeadBranch: "branch"}
				switch invalid {
				case "head":
					pr.HeadSHA = f.base
				case "author":
					pr.AuthorEmail = ""
				case "title":
					pr.Title = "Title\nInjected"
				case "error":
					return pr, fmt.Errorf("metadata unavailable")
				}
				return pr, nil
			}
			_, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/invalid", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Squash, BatchID: "invalid"})
			if err == nil {
				t.Fatal("invalid metadata accepted")
			}
			if _, err := f.g.ReadRef(t.Context(), testRepo, "openmerge/queue/main/invalid"); err == nil {
				t.Fatal("invalid metadata published a ref")
			}
		})
	}
}
