package forgetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func invalidInput(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, forge.ErrInvalidInput) || errors.Is(err, forge.ErrLeaseRejected) || errors.Is(err, forge.ErrConflict) || errors.Is(err, forge.ErrNotFastForward) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

// These cases execute through both implementations, rather than calling only
// the extracted validators. Every rejection must preserve the leased refs.
func runGitValidation(t *testing.T, factory func(*testing.T) Env) {
	t.Helper()
	t.Run("invalid-inputs", func(t *testing.T) {
		e := factory(t)
		ctx := t.Context()
		build := forge.BuildRequest{Ref: "openmerge/queue/" + e.Base + "/validation", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: forge.Merge, BatchID: "validation"}
		land := forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: e.PRs[0].HeadSHA, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/validation", PRs: e.PRs}
		for i, ref := range []string{"", "refs/tags/tag", "refs/heads/", ".hidden", "bad..name", "bad@{name", "bad name", "bad\nname", "bad\x00name", "bad\\name", "bad~name", "bad^name", "bad:name", "bad?name", "bad*name", "bad[name", "bad\x7fname", "bad//name", "bad/.name", "bad/name.lock", "bad/name."} {
			t.Run(fmt.Sprintf("ref/%d", i), func(t *testing.T) {
				req := build
				req.Ref = ref
				_, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
				invalidInput(t, err)
				_, err = e.Git.ReadRef(ctx, e.Repo, ref)
				invalidInput(t, err)
				_, _, _, err = e.Git.ReadFence(ctx, e.Repo, ref)
				invalidInput(t, err)
				_, err = e.Git.EnsureFence(ctx, e.Repo, ref, forge.PushOptions{})
				invalidInput(t, err)
				_, err = e.Git.AdvanceFence(ctx, e.Repo, ref, e.FenceSHA, "abandon/validation", forge.PushOptions{})
				invalidInput(t, err)
				lr := land
				lr.Base = ref
				_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
				invalidInput(t, err)
				lr = land
				lr.Fence = ref
				_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
				invalidInput(t, err)
			})
		}
		for i, branch := range []string{"", "refs/heads/topic", "refs/tags/tag", "openmerge/queue/topic", "bad..name", "bad name", "topic.lock"} {
			t.Run(fmt.Sprintf("delete-branch/%d", i), func(t *testing.T) {
				invalidInput(t, e.Git.DeleteBranch(ctx, e.Repo, branch, e.BaseSHA, forge.PushOptions{}))
			})
		}
		for i, sha := range []string{"", "main", "--all", strings.Repeat("a", 39), strings.Repeat("g", 40), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
			t.Run(fmt.Sprintf("sha/%d", i), func(t *testing.T) {
				invalidInput(t, e.Git.DeleteBranch(ctx, e.Repo, "missing", sha, forge.PushOptions{}))
				_, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, sha, "abandon/validation", forge.PushOptions{})
				invalidInput(t, err)
				for pos := range 2 {
					pair := []string{e.BaseSHA, e.PRs[0].HeadSHA}
					pair[pos] = sha
					_, err = e.Git.IsAncestor(ctx, e.Repo, pair[0], pair[1])
					invalidInput(t, err)
					_, err = e.Git.HasMergeCommits(ctx, e.Repo, pair[0], pair[1])
					invalidInput(t, err)
					for _, reader := range []func(context.Context, forge.RepoRef, string, string, int) ([]string, bool, error){e.Git.(forge.CommitHistoryReader).CommitsBetween, e.Git.(forge.FirstParentCommitReader).FirstParentCommits} {
						_, _, err = reader(ctx, e.Repo, pair[0], pair[1], 1)
						invalidInput(t, err)
					}
				}
				if sha != "" {
					lr := land
					lr.FastForwardHeadSHA = sha
					_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
					invalidInput(t, err)
				}
				for pos := range 3 {
					triple := []string{e.BaseSHA, e.PRs[0].HeadSHA, e.PRs[0].HeadSHA}
					triple[pos] = sha
					invalidInput(t, e.Git.VerifyFastForward(ctx, e.Repo, triple[0], triple[1], triple[2]))
					lr := land
					switch pos {
					case 0:
						lr.ExpectedBaseSHA = sha
					case 1:
						lr.TestedSHA = sha
					case 2:
						lr.ExpectedFence = sha
					}
					_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
					invalidInput(t, err)
				}
				req := build
				req.BaseSHA = sha
				_, err = e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
				invalidInput(t, err)
				req = build
				req.PRs = []forge.BuildPR{{Number: e.PRs[0].Number, HeadSHA: sha}}
				_, err = e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
				invalidInput(t, err)
				lr := land
				lr.PRs = req.PRs
				_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
				invalidInput(t, err)
			})
		}
		for _, id := range []string{"", "two tokens", "tab\tid", "cr\rid", "lf\nid", "nul\x00id"} {
			_, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, id, forge.PushOptions{})
			invalidInput(t, err)
			lr := land
			lr.NextFenceID = id
			_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
			invalidInput(t, err)
		}
		for _, tc := range []struct {
			name     string
			metadata []*forge.ChangeMetadata
		}{
			{name: "empty", metadata: []*forge.ChangeMetadata{{}}},
			{name: "invalid-label", metadata: []*forge.ChangeMetadata{{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1, Labels: []string{"docs\nforged"}}}},
			{name: "missing", metadata: []*forge.ChangeMetadata{{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1}, nil}},
			{name: "duplicate", metadata: []*forge.ChangeMetadata{{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1}, {ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 2}}},
		} {
			t.Run("build-metadata/"+tc.name, func(t *testing.T) {
				req := build
				req.PRs = append([]forge.BuildPR(nil), e.PRs[:len(tc.metadata)]...)
				for i, metadata := range tc.metadata {
					req.PRs[i].Metadata = metadata
				}
				_, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
				invalidInput(t, err)
			})
		}
		for _, number := range []int{-1, 0} {
			req := build
			req.PRs = []forge.BuildPR{{Number: number, HeadSHA: e.PRs[0].HeadSHA}}
			_, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
			invalidInput(t, err)
			lr := land
			lr.PRs = req.PRs
			_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
			invalidInput(t, err)
		}
		for _, bad := range []string{"", "bad/id", "two tokens"} {
			req := build
			req.BatchID = bad
			_, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
			invalidInput(t, err)
		}
		req := build
		req.PRs = nil
		_, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
		invalidInput(t, err)
		req = build
		req.Method = "invalid"
		_, err = e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
		invalidInput(t, err)
		lr := land
		lr.Fence = "refs/heads/" + e.Base
		_, err = e.Git.Land(ctx, e.Repo, lr, forge.PushOptions{})
		invalidInput(t, err)
		for _, pair := range [][2]string{{e.Base, e.BaseSHA}, {e.FenceRef, e.FenceSHA}} {
			got, err := e.Git.ReadRef(ctx, e.Repo, pair[0])
			must(t, err)
			if got != pair[1] {
				t.Fatalf("rejected operation mutated %s: %s", pair[0], got)
			}
		}
		if _, err := e.Git.ReadRef(ctx, e.Repo, build.Ref); err == nil {
			t.Fatal("rejected build created a ref")
		}
	})
	t.Run("fence-token-before-stale-lease", func(t *testing.T) {
		e := factory(t)
		ctx := t.Context()
		next, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "abandon/validation", forge.PushOptions{})
		must(t, err)
		_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: e.PRs[0].HeadSHA, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "two tokens", PRs: e.PRs}, forge.PushOptions{})
		invalidInput(t, err)
		for _, pair := range [][2]string{{e.Base, e.BaseSHA}, {e.FenceRef, next}} {
			got, err := e.Git.ReadRef(ctx, e.Repo, pair[0])
			must(t, err)
			if got != pair[1] {
				t.Fatalf("rejected landing mutated %s: %s", pair[0], got)
			}
		}
	})
	t.Run("qualified-branch-aliases", func(t *testing.T) {
		e := factory(t)
		ctx := t.Context()
		sha, err := e.Git.ReadRef(ctx, e.Repo, "refs/heads/"+e.Base)
		must(t, err)
		if sha != e.BaseSHA {
			t.Fatal("ReadRef alias differs")
		}
		sha, err = e.Git.EnsureFence(ctx, e.Repo, "refs/heads/"+e.FenceRef, forge.PushOptions{})
		must(t, err)
		if sha != e.FenceSHA {
			t.Fatal("EnsureFence alias differs")
		}
		next, err := e.Git.AdvanceFence(ctx, e.Repo, "refs/heads/"+e.FenceRef, e.FenceSHA, "abandon/alias", forge.PushOptions{})
		must(t, err)
		sha, id, _, err := e.Git.ReadFence(ctx, e.Repo, e.FenceRef)
		must(t, err)
		if sha != next || id != "abandon/alias" {
			t.Fatal("Advance/ReadFence alias differs")
		}
		ref := "openmerge/queue/" + e.Base + "/alias"
		tested, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: "refs/heads/" + ref, BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "alias"}, forge.PushOptions{})
		must(t, err)
		sha, err = e.Git.ReadRef(ctx, e.Repo, ref)
		must(t, err)
		if sha != tested {
			t.Fatal("BuildTestRef alias differs")
		}
		_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: "refs/heads/" + e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: tested, Fence: "refs/heads/" + e.FenceRef, ExpectedFence: next, NextFenceID: "land/alias", PRs: e.PRs[:1]}, forge.PushOptions{})
		must(t, err)
	})
}

func runDriverValidation(t *testing.T, factory func(*testing.T) Env) {
	t.Helper()
	t.Run("invalid-inputs", func(t *testing.T) {
		e := factory(t)
		ctx := t.Context()
		pr := e.PRs[0].Number
		head := e.PRs[0].HeadSHA
		preserved := e.NewBase(t)
		for _, key := range []string{"", "bad<key", "bad>key", "bad\rkey", "bad\nkey", "bad--key"} {
			invalidInput(t, e.Driver.Comment(ctx, e.Repo, pr, key, "body"))
		}
		for _, user := range []string{"", "bad/user", "bad\\user", "bad?user", "bad#user", "bad%user", "bad\ruser", "bad\nuser"} {
			allowed, err := e.Driver.CanWrite(ctx, e.Repo, user)
			invalidInput(t, err)
			if allowed {
				t.Fatal("invalid login authorized")
			}
		}
		for _, state := range []forge.StatusState{"", "unknown", "success", "building"} {
			invalidInput(t, e.Driver.ReportStatus(ctx, e.Repo, pr, head, forge.Status{State: state}))
		}
		for _, pair := range [][2]string{{"", "file"}, {e.Base, ""}, {"", ""}} {
			_, err := e.Driver.ReadFile(ctx, e.Repo, pair[0], pair[1])
			invalidInput(t, err)
		}
		for _, ref := range []string{"", "bad..name", "refs/heads/", "heads/bad name", "refs/tags/tag.lock"} {
			invalidInput(t, e.Driver.DeleteRef(ctx, e.Repo, ref))
		}
		for _, number := range []int{-1, 0} {
			_, err := e.Driver.PullRequest(ctx, e.Repo, number)
			invalidInput(t, err)
			_, err = e.Driver.CommitAuthors(ctx, e.Repo, number)
			invalidInput(t, err)
			_, err = e.Driver.HeadBranchForDeletion(ctx, e.Repo, number, head, e.Base)
			invalidInput(t, err)
			_, err = e.Driver.ListComments(ctx, e.Repo, number, time.Time{})
			invalidInput(t, err)
			invalidInput(t, e.Driver.ReportStatus(ctx, e.Repo, number, head, forge.Status{State: forge.Queued}))
			invalidInput(t, e.Driver.Comment(ctx, e.Repo, number, "valid", "body"))
			invalidInput(t, e.Driver.MarkLanded(ctx, e.Repo, number, head))
		}
		invalidInput(t, e.Driver.MarkLanded(ctx, e.Repo, pr, "bad--sha"))
		if len(e.Comments(pr)) != 0 || len(e.Statuses(pr)) != 0 {
			t.Fatal("invalid input mutated comments or statuses")
		}
		if e.RefExists != nil && !e.RefExists(preserved) {
			t.Fatal("invalid deletion removed base")
		}
	})
	// Cancellation is the only forge-neutral invalid input for observations such
	// as DefaultBranch and CheckRuns whose values are otherwise API-owned.
	t.Run("cancelled-inputs", func(t *testing.T) {
		e := factory(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		pr := e.PRs[0].Number
		head := e.PRs[0].HeadSHA
		calls := map[string]func() error{
			"DefaultBranch":         func() error { _, err := e.Driver.DefaultBranch(ctx, e.Repo); return err },
			"ReadFile":              func() error { _, err := e.Driver.ReadFile(ctx, e.Repo, e.Base, "file"); return err },
			"PullRequest":           func() error { _, err := e.Driver.PullRequest(ctx, e.Repo, pr); return err },
			"CommitAuthors":         func() error { _, err := e.Driver.CommitAuthors(ctx, e.Repo, pr); return err },
			"HeadBranchForDeletion": func() error { _, err := e.Driver.HeadBranchForDeletion(ctx, e.Repo, pr, head, e.Base); return err },
			"ListOpenPullRequests":  func() error { _, err := e.Driver.ListOpenPullRequests(ctx, e.Repo, ""); return err },
			"ListComments":          func() error { _, err := e.Driver.ListComments(ctx, e.Repo, pr, time.Time{}); return err },
			"CanWrite":              func() error { _, err := e.Driver.CanWrite(ctx, e.Repo, "writer"); return err },
			"CheckRuns":             func() error { _, err := e.Driver.CheckRuns(ctx, e.Repo, head); return err },
			"DeleteRef":             func() error { return e.Driver.DeleteRef(ctx, e.Repo, e.Base) },
			"ReportStatus":          func() error { return e.Driver.ReportStatus(ctx, e.Repo, pr, head, forge.Status{State: forge.Queued}) },
			"MarkLanded":            func() error { return e.Driver.MarkLanded(ctx, e.Repo, pr, head) },
			"Comment":               func() error { return e.Driver.Comment(ctx, e.Repo, pr, "valid", "body") },
		}
		for name, call := range calls {
			t.Run(name, func(t *testing.T) {
				if err := call(); !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v, want context.Canceled", err)
				}
			})
		}
	})

	t.Run("landed-comment-key-retry", func(t *testing.T) {
		e := factory(t)
		pr := e.PRs[0]
		ctx := t.Context()
		must(t, e.Driver.MarkLanded(ctx, e.Repo, pr.Number, pr.HeadSHA))
		must(t, e.Driver.Comment(ctx, e.Repo, pr.Number, "landed/"+pr.HeadSHA, "duplicate"))
		comments, err := e.Driver.ListComments(ctx, e.Repo, pr.Number, time.Time{})
		must(t, err)
		if len(comments) != 1 || !strings.Contains(comments[0].Body, "<!-- openmerge:key=landed/"+pr.HeadSHA+" -->") {
			t.Fatalf("landed comment retry=%+v", comments)
		}
	})
	t.Run("delete-ref-aliases", func(t *testing.T) {
		e := factory(t)
		for _, prefix := range []string{"heads/", "refs/heads/"} {
			ref := e.NewBase(t)
			must(t, e.Driver.DeleteRef(t.Context(), e.Repo, prefix+ref))
			must(t, e.Driver.DeleteRef(t.Context(), e.Repo, prefix+ref))
			if e.RefExists(ref) {
				t.Fatalf("DeleteRef(%s%s) preserved ref", prefix, ref)
			}
		}
	})
}
