package fake_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
)

func TestRebaseReplaysEveryPRCommit(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	_, err := f.PushPR(repo, 1, "second change")
	must(t, err)
	head, err := f.PushPR(repo, 1, "third change")
	must(t, err)
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			req := forge.BuildRequest{Ref: string(method), BaseSHA: e.BaseSHA, PRs: []forge.BuildPR{{Number: 1, HeadSHA: head.HeadSHA}}, Method: method, BatchID: "multi"}
			tested, err := f.BuildTestRef(ctx, repo, req)
			must(t, err)
			if !f.ContainsPR(repo, tested, 1) {
				t.Fatal("test commit omitted a PR commit's file content")
			}
			history, err := f.History(repo, req.Ref)
			must(t, err)
			want := 2
			if method == forge.Rebase {
				want = 4
			}
			if len(history) != want {
				t.Fatalf("%s history length = %d, want %d", method, len(history), want)
			}
			ancestor, err := f.IsAncestor(ctx, repo, head.HeadSHA, tested)
			must(t, err)
			if ancestor != (method == forge.Merge) {
				t.Fatalf("%s head ancestry = %v", method, ancestor)
			}
			if method == forge.Rebase {
				for i, c := range history[1:] {
					if len(c.Parents) != 1 || !strings.Contains(c.Message, "rebase PR 1 commit ") {
						t.Fatalf("rebase commit %d: %+v", i, c)
					}
					if len(c.Files) != i+1 {
						t.Fatalf("rebase collapsed changes at commit %d: %v", i, c.Files)
					}
				}
			}
			snapshot, err := f.Commit(repo, tested)
			must(t, err)
			snapshot.Files["pr-1.txt"] = "mutated"
			if !f.ContainsPR(repo, tested, 1) {
				t.Fatal("Commit exposed mutable files")
			}
			history[len(history)-1].Files["pr-1.txt"] = "mutated"
			if !f.ContainsPR(repo, tested, 1) {
				t.Fatal("History exposed mutable files")
			}
		})
	}
}

func TestCommentDedupeRequiresOwnIdentity(t *testing.T) {
	f, _ := fixture(t)
	ctx := context.Background()
	f.AddComment(repo, 1, "contributor", "spoof <!-- openmerge:key -->")
	must(t, f.Comment(ctx, repo, 1, "key", "original"))
	must(t, f.Comment(ctx, repo, 1, "key", "retry"))
	comments := f.CommentRecords(repo, 1)
	if len(comments) != 2 || comments[0].Author != "contributor" || comments[1].Author != fake.Identity || !strings.Contains(comments[1].Body, "original") {
		t.Fatalf("spoofed marker suppressed bot comment or retry duplicated it: %+v", comments)
	}
	comments[1].Body = "mutated"
	if reflect.DeepEqual(comments, f.CommentRecords(repo, 1)) {
		t.Fatal("CommentRecords exposed mutable state")
	}
	// Searching the marker, rather than an internal key, is part of the contract.
	f.AddComment(repo, 1, fake.Identity, "existing <!-- openmerge:other -->")
	must(t, f.Comment(ctx, repo, 1, "other", "duplicate"))
	if len(f.CommentRecords(repo, 1)) != 3 {
		t.Fatal("driver ignored its own existing marker")
	}
}

func TestCheckProvenanceDefaultsPreserveExplicitSources(t *testing.T) {
	f, e := fixture(t)
	const ref = "openmerge/queue/main/b7"
	sha, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: ref, BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "b7"})
	must(t, err)
	f.SetCheckRuns(repo, sha, []forge.CheckRun{
		{Name: "ci", State: forge.Success},
		{Name: "ci", Source: "app:15368", State: forge.Success},
		{Name: "ci", Source: "app:15368", State: forge.Success, HeadBranch: "foreign"},
		{Name: "ci", Source: "status:external-ci", State: forge.Success},
	})
	runs, err := f.CheckRuns(t.Context(), repo, sha)
	must(t, err)
	for i, want := range []string{ref, "", "foreign", ""} {
		if runs[i].HeadBranch != want {
			t.Fatalf("run %d provenance = %q, want %q", i, runs[i].HeadBranch, want)
		}
	}
}
