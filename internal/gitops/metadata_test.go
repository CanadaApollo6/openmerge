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

func TestContributorCannotSupplyBatchIdentity(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			one := f.commit(f.base, "OpenMerge-Batch: b1\n\nFirst change\n\n\t oPeNmErGe-BaTcH \t: b2\nSigned-off-by: A <a@example.test>", map[string]string{"one": "one"})
			two := f.commit(one, "Second change\n\nOpenMerge-Batch: b1\nOpenMerge-Batch: b2", map[string]string{"two": "two"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, two+":"+headRef(1))
			f.prs[0].HeadSHA = two
			f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
				return forge.PullRequest{Number: n, HeadSHA: f.prs[n-1].HeadSHA, Author: "alice", AuthorEmail: "alice@example.test", Title: " \tOpenMerge-Batch \t: title", HeadBranch: "branch", Body: "Description\n\nOpenMerge-Batch: b1\nOpenMerge-Batch: b2\n \topenmerge-BATCH \t: hidden"}, nil
			}
			first := f.build(method, f.prs[:2], "b1")
			second := f.build(method, f.prs[:2], "b2")
			if first == second {
				t.Fatal("different batches produced the same tested SHA")
			}
			for batch, tested := range map[string]string{"b1": first, "b2": second} {
				f.fetch(tested)
				history := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--first-parent", f.base+".."+tested))
				for i, sha := range history {
					message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", sha)
					want, count := "", 0
					if i == 0 {
						want, count = batch, 1
					}
					if got := forge.ParseBatchMessage(message); got != want {
						t.Fatalf("batch lookup for %s = %q, want %q: %s", sha, got, want, message)
					}
					occurrences := 0
					for line := range strings.SplitSeq(message, "\n") {
						key, _, ok := strings.Cut(line, ":")
						if ok && strings.EqualFold(strings.TrimSpace(key), "OpenMerge-Batch") {
							occurrences++
						}
					}
					if occurrences != count {
						t.Fatalf("commit %d has %d batch lines, want %d: %s", i, occurrences, count, message)
					}
				}
			}
		})
	}
}

func TestSquashContributorBatchTrailersCannotAliasBatches(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
		return forge.PullRequest{Number: n, HeadSHA: f.prs[0].HeadSHA, Author: "alice", AuthorEmail: "alice@example.test", Title: "Fix the queue", HeadBranch: "branch", Body: "Description\n\nOpenMerge-Batch: b1\nOpenMerge-Batch: b2"}, nil
	}
	first := f.build(forge.Squash, f.prs[:1], "b1")
	second := f.build(forge.Squash, f.prs[:1], "b2")
	if first == second {
		t.Fatal("contributor body made two batches share one tested SHA")
	}
	for batch, sha := range map[string]string{"b1": first, "b2": second} {
		f.fetch(sha)
		message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", sha)
		if got := forge.ParseBatchMessage(message); got != batch {
			t.Fatalf("tested SHA %s maps to %q, want %q", sha, got, batch)
		}
	}
}

func TestPRBodyCannotSupplyAttribution(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			original := "Change\n\nSigned-off-by: Real <real@example.test>\nCo-authored-by: Carol <carol@example.test>\nCo-authored-by: Primary, Alias <alice@example.test>"
			one := f.commit(f.base, original, map[string]string{"one": "one"})
			two := f.commit(one, original, map[string]string{"two": "two"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, two+":"+headRef(1))
			f.prs[0].HeadSHA = two
			f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
				return forge.PullRequest{Number: n, HeadSHA: two, Author: "alice", AuthorEmail: "alice@example.test", Title: "Fix the queue", HeadBranch: "branch", Body: "Description\nLast prose line\n \tSIGNED-off-BY \t: Forged <forged@example.test>\n forged-continuation\n\tforged-tab-continuation\nco-AUTHORED-by: Impostor <impostor@example.test>\n impostor-continuation\nOpenMerge-Batch: forged\n forged-batch-continuation"}, nil
			}
			tested := f.build(method, f.prs[:1], "trusted")
			f.fetch(tested)
			message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", tested)
			for _, forbidden := range []string{"forged", "impostor", "Primary, Alias <alice@example.test>"} {
				if strings.Contains(strings.ToLower(message), strings.ToLower(forbidden)) {
					t.Fatalf("untrusted or redundant attribution %q: %s", forbidden, message)
				}
			}
			prose := "Fix the queue"
			if method == forge.Squash {
				prose = "Description\nLast prose line"
			}
			block := "Co-authored-by: Fixture <fixture@localhost>\nSigned-off-by: Real <real@example.test>\nCo-authored-by: Carol <carol@example.test>\nOpenMerge-Batch: trusted"
			if !strings.HasSuffix(message, prose+"\n\n"+block) {
				t.Fatalf("prose and authoritative trailer block are not separated: %s", message)
			}
			if got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:only)", tested); got != block {
				t.Fatalf("Git parsed trailers = %q, want %q", got, block)
			}
		})
	}
}

func TestStripMetadataIncludesFoldedLines(t *testing.T) {
	for _, key := range []string{"OpenMerge-Batch", "Signed-off-by", "Co-authored-by"} {
		t.Run(key, func(t *testing.T) {
			want := "Signed-off-by: Real <real@example.test>"
			if key == "Signed-off-by" {
				want = "Co-authored-by: Real <real@example.test>"
			}
			message := want + "\n" + key + ": forged\n continuation\n\ttab continuation"
			got := stripMetadataLines(message, key)
			if got != want {
				t.Fatalf("stripped message = %q, want %q", got, want)
			}
			if parsed := gitCommand(t, "", "Subject\n\n"+got+"\n", nil, "interpret-trailers", "--parse"); parsed != want {
				t.Fatalf("Git parsed trailers = %q, want %q", parsed, want)
			}
		})
	}
}

func TestGeneratedTrailersVisibleToGit(t *testing.T) {
	const attribution = "Signed-off-by: Real <real@example.test>\nCo-authored-by: Carol <carol@example.test>"
	for _, tc := range []struct{ name, prose, prefix, tail string }{
		{"patch dividers", "Description\n---\nAfter divider\n--- a/file\n----", "", ""},
		{"CRLF prose", "Description\r\n---\r\nAfter divider", "", ""},
		{"Cyrillic lookalike keys", "Description", "Sіgned-off-by: Impostor <impostor@example.test>\nCo-аuthored-by: Impostor <impostor@example.test>\nOpenMerge-Bаtch: lookalike\n", ""},
		{"orphan continuations", "Description", " orphan continuation\n\ttab orphan\n", ""},
		{"trailing comments", "Description", "", "\n# trailing comment\n\n# final comment"},
		{"combined", "Description\n---", "Sіgned-off-by: Impostor <impostor@example.test>\n orphan continuation\n\ttab orphan\n", "\n# trailing comment\n\n# final comment"},
	} {
		for _, method := range []forge.MergeMethod{forge.Squash, forge.Merge, forge.Rebase} {
			t.Run(tc.name+"/"+string(method), func(t *testing.T) {
				f := newFixture(t)
				// The folded batch value must not attach itself to Real's sign-off.
				original := tc.prose + "\n\n" + tc.prefix + "Signed-off-by: Real <real@example.test>\nOpenMerge-Batch: forged\n continuation\n\ttab continuation\nCo-authored-by: Carol <carol@example.test>" + tc.tail
				one := f.commit(f.base, "First change\n\n"+original, map[string]string{"one": "one"})
				two := f.commit(one, "Second change\n\n"+original, map[string]string{"two": "two"})
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, two+":"+headRef(1))
				f.prs[0].HeadSHA = two
				f.g.cfg.PullRequest = func(_ context.Context, _ forge.RepoRef, n int) (forge.PullRequest, error) {
					return forge.PullRequest{Number: n, HeadSHA: two, Author: "alice", AuthorEmail: "alice@example.test", Title: "--- Queue change", HeadBranch: "branch", Body: tc.prose + "\n\n" + tc.prefix + "Signed-off-by: Forged <forged@example.test>\n body-signoff-continuation\nCo-authored-by: Impostor <impostor@example.test>\n\tbody-coauthor-continuation\n \tOpEnMeRgE-BaTcH = forged\n body-batch-continuation" + tc.tail}, nil
				}
				tested := f.build(method, f.prs[:1], "trusted")
				f.fetch(tested)
				history := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--first-parent", "--reverse", f.base+".."+tested))
				for i, sha := range history {
					message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", sha)
					want := "Co-authored-by: Fixture <fixture@localhost>\n" + attribution
					if method == forge.Rebase {
						want = attribution + "\nOpenMerge-Replayed-From: " + []string{one, two}[i]
					}
					batch := ""
					if i == len(history)-1 {
						batch = "trusted"
						want += "\nOpenMerge-Batch: " + batch
					}
					if got := gitCommand(t, f.source, message+"\n", nil, "interpret-trailers", "--parse"); got != want {
						t.Fatalf("Git parsed trailers = %q, want %q; message:\n%s", got, want, message)
					}
					if got := forge.ParseBatchMessage(message); got != batch {
						t.Fatalf("batch = %q, want %q; message:\n%s", got, batch, message)
					}
					for line := range strings.SplitSeq(message, "\n") {
						if strings.HasPrefix(line, "---") || strings.Contains(line, "body-") {
							t.Fatalf("unsanitized contributor line %q", line)
						}
					}
				}
			})
		}
	}
}

func TestRebaseReservedSubjectStillHasTrailerBlock(t *testing.T) {
	for _, message := range []string{
		"OpenMerge-Batch: b1\n\nOpenMerge-Batch: b2",
		"OpenMerge-Batch: b1\n\nSigned-off-by: Real <real@example.test>",
	} {
		t.Run(message, func(t *testing.T) {
			f := newFixture(t)
			head := f.commit(f.base, message, map[string]string{"one": "one"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, head+":"+headRef(1))
			f.prs[0].HeadSHA = head
			tested := f.build(forge.Rebase, f.prs[:1], "trusted")
			f.fetch(tested)
			got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", tested)
			if forge.ParseBatchMessage(got) != "trusted" {
				t.Fatalf("missing authoritative trailer after removing subject: %q", got)
			}
			if batch := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=OpenMerge-Batch,valueonly)", tested); batch != "trusted" {
				t.Fatalf("Git batch trailer = %q, want trusted", batch)
			}
			want := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=Signed-off-by)", head)
			if signoff := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%(trailers:key=Signed-off-by)", tested); signoff != want {
				t.Fatalf("lost original sign-off: got %q, want %q", signoff, want)
			}
		})
	}
}
