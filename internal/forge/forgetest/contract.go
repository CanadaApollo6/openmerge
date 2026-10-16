// Package forgetest provides the contract suite shared by forge implementations.
package forgetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Env supplies an isolated repository with a base root, a fence root and at least
// two PRs adding unique files, and a separate pair adding conflicting file content.
// Observations includes a fork PR and a same-repository PR with exact approvals.
// Hooks perform sandbox setup and inspect API write effects;
// they may close over t to report setup/inspection failures on real forges.
type Env struct {
	Driver                            forge.Driver
	Git                               forge.Git
	Repo                              forge.RepoRef
	Base, FenceRef, BaseSHA, FenceSHA string
	PRs                               []forge.BuildPR
	SetRef                            func(ref, sha string) error
	ConflictingPRs                    []forge.BuildPR
	Observations                      []forge.PullRequest
	ContainsPR                        func(sha string, pr int) bool
	AddForeignComment                 func(pr int, body string)
	SetCheckRuns                      func(sha string, checks []forge.CheckRun)
	Statuses                          func(pr int) map[string]forge.Status
	// Comments returns only driver-authored comments, keyed by marker.
	Comments func(pr int) map[string]string
	// Landed verifies native merged evidence or exactly one closing commit link.
	Landed func(pr int, sha string) bool
}

// Run checks observable contracts. Every subtest receives a fresh environment.
// Repeated API writes model a crash after the effect but before its recording.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	t.Helper()
	factory := newEnv
	newEnv = func(t *testing.T) Env {
		t.Helper()
		e := factory(t)
		if len(e.PRs) < 2 || len(e.ConflictingPRs) != 2 {
			t.Fatal("contract needs unique-file PRs and a conflicting pair")
		}
		fork, sameRepo := false, false
		for _, pr := range e.Observations {
			fork = fork || !pr.HeadInBaseRepo && len(pr.Approvals) > 0
			sameRepo = sameRepo || pr.HeadInBaseRepo && len(pr.Approvals) > 0
		}
		if !fork || !sameRepo {
			t.Fatal("contract needs fork and same-repo observations")
		}
		return e
	}
	ctx := context.Background()
	build := func(t *testing.T, e Env, base, batch string, prs ...forge.BuildPR) string {
		t.Helper()
		sha, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
			Ref: "openmerge/queue/contract/" + batch, BaseSHA: base,
			PRs: prs, Method: forge.Merge, BatchID: batch,
		})
		must(t, err)
		if sha == "" {
			t.Fatal("BuildTestRef returned an empty SHA")
		}
		return sha
	}
	read := func(t *testing.T, e Env, ref string) string {
		t.Helper()
		sha, err := e.Git.ReadRef(ctx, e.Repo, ref)
		must(t, err)
		return sha
	}
	readFence := func(t *testing.T, e Env, wantSHA, wantID string) {
		t.Helper()
		for range 2 {
			sha, id, tested, err := e.Git.ReadFence(ctx, e.Repo, e.FenceRef)
			must(t, err)
			if sha != wantSHA || id != wantID || strings.HasPrefix(id, "land/") && tested == "" {
				t.Fatalf("ReadFence = (%q, %q), want (%q, %q)", sha, id, wantSHA, wantID)
			}
		}
	}
	t.Run("read-fence", func(t *testing.T) {
		e := newEnv(t)
		must(t, e.SetRef(e.FenceRef, e.BaseSHA))
		readFence(t, e, e.BaseSHA, "")
		if _, _, _, err := e.Git.ReadFence(ctx, e.Repo, "openmerge/fence/missing"); err == nil {
			t.Fatal("ReadFence accepted a missing ref")
		}
	})
	for _, moved := range []string{"matching", "forward", "rollback", "fence"} {
		t.Run("land/"+moved, func(t *testing.T) {
			e := newEnv(t)
			current := build(t, e, e.BaseSHA, "current", e.PRs[0])
			must(t, e.SetRef(e.Base, current))
			tested := build(t, e, current, "tested", e.PRs[1])
			switch moved {
			case "forward":
				must(t, e.SetRef(e.Base, build(t, e, current, "forward", e.PRs[1])))
			case "rollback":
				must(t, e.SetRef(e.Base, e.BaseSHA))
			case "fence":
				_, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "other-worker")
				must(t, err)
			}
			beforeBase, beforeFence := read(t, e, e.Base), read(t, e, e.FenceRef)
			req := forge.LandRequest{
				Base: e.Base, ExpectedBaseSHA: current, TestedSHA: tested,
				Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/contract",
			}
			next, err := e.Git.Land(ctx, e.Repo, req)
			if moved == "matching" {
				must(t, err)
				if next == "" || next == e.FenceSHA || read(t, e, e.Base) != tested || read(t, e, e.FenceRef) != next {
					t.Fatal("Land did not move both refs to the tested commit and returned fence SHA")
				}
				readFence(t, e, next, req.NextFenceID)
				// An unrecorded successful push must refuse its old leases on retry.
				beforeBase, beforeFence = tested, next
				_, err = e.Git.Land(ctx, e.Repo, req)
			}
			if !errors.Is(err, forge.ErrLeaseRejected) {
				t.Fatalf("Land error = %v, want lease rejection", err)
			}
			if read(t, e, e.Base) != beforeBase || read(t, e, e.FenceRef) != beforeFence {
				t.Fatal("rejected Land changed a ref")
			}
		})
	}
	t.Run("advance-fence", func(t *testing.T) {
		e := newEnv(t)
		next, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "abandon/contract")
		must(t, err)
		if next == "" || next == e.FenceSHA || read(t, e, e.FenceRef) != next || read(t, e, e.Base) != e.BaseSHA {
			t.Fatal("AdvanceFence did not return the new fence SHA while preserving base")
		}
		readFence(t, e, next, "abandon/contract")
		_, err = e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "stale")
		if !errors.Is(err, forge.ErrLeaseRejected) {
			t.Fatalf("stale fence error = %v, want current fence %s", err, next)
		}
		if read(t, e, e.FenceRef) != next || read(t, e, e.Base) != e.BaseSHA {
			t.Fatal("rejected AdvanceFence changed refs")
		}
		// A delayed landing using the abandoned attempt's token is now impossible.
		tested := build(t, e, e.BaseSHA, "late", e.PRs[0])
		_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{
			Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: tested,
			Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "late",
		})
		if !errors.Is(err, forge.ErrLeaseRejected) || read(t, e, e.Base) != e.BaseSHA || read(t, e, e.FenceRef) != next {
			t.Fatalf("fenced delayed landing was not refused atomically: %v", err)
		}
	})
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run("unique-test-commit/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			req := forge.BuildRequest{Ref: "openmerge/queue/contract/unique", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "first"}
			first, err := e.Git.BuildTestRef(ctx, e.Repo, req)
			must(t, err)
			ancestor, err := e.Git.IsAncestor(ctx, e.Repo, e.BaseSHA, first)
			must(t, err)
			if !ancestor {
				t.Fatal("base is not an ancestor of test commit")
			}
			for _, pr := range e.PRs {
				if !e.ContainsPR(first, pr.Number) {
					t.Fatalf("test commit omits PR %d content", pr.Number)
				}
				ancestor, err := e.Git.IsAncestor(ctx, e.Repo, pr.HeadSHA, first)
				must(t, err)
				if ancestor != (method == forge.Merge) {
					t.Fatalf("%s PR %d ancestry = %v", method, pr.Number, ancestor)
				}
			}
			req.BatchID = "second"
			second, err := e.Git.BuildTestRef(ctx, e.Repo, req)
			must(t, err)
			if first == second || read(t, e, req.Ref) != second {
				t.Fatal("different batch IDs reused a top commit, or ref was not pushed")
			}
		})
	}
	t.Run("conflict", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
			Ref: "openmerge/queue/contract/conflict", BaseSHA: e.BaseSHA, PRs: e.ConflictingPRs, Method: forge.Merge, BatchID: "conflict",
		})
		if !errors.Is(err, forge.ErrConflict) {
			t.Fatalf("BuildTestRef error = %v, want ErrConflict", err)
		}
	})
	t.Run("delete-ref", func(t *testing.T) {
		e := newEnv(t)
		build(t, e, e.BaseSHA, "delete", e.PRs[0])
		for range 2 {
			must(t, e.Driver.DeleteRef(ctx, e.Repo, "openmerge/queue/contract/delete"))
		}
		if _, err := e.Git.ReadRef(ctx, e.Repo, "openmerge/queue/contract/delete"); err == nil {
			t.Fatal("deleted ref still exists")
		}
		must(t, e.Driver.DeleteRef(ctx, e.Repo, "never-existed"))
	})
	t.Run("comment-retry", func(t *testing.T) {
		e := newEnv(t)
		pr := e.PRs[0].Number
		e.AddForeignComment(pr, "<!-- openmerge:action-1 -->")
		must(t, e.Driver.Comment(ctx, e.Repo, pr, "action-1", "original"))
		must(t, e.Driver.Comment(ctx, e.Repo, pr, "action-1", "retry"))
		comments := e.Comments(pr)
		if len(comments) != 1 || !strings.Contains(comments["action-1"], "original") || !strings.Contains(comments["action-1"], "<!--") || !strings.Contains(comments["action-1"], "action-1") {
			t.Fatalf("comment retry did not preserve one marked original: %v", comments)
		}
		must(t, e.Driver.Comment(ctx, e.Repo, pr, "action-2", "another"))
		if len(e.Comments(pr)) != 2 {
			t.Fatal("distinct comment keys were collapsed")
		}
	})
	t.Run("status-upsert", func(t *testing.T) {
		e := newEnv(t)
		pr := e.PRs[0].Number
		head := e.PRs[0].HeadSHA
		otherHead := e.PRs[1].HeadSHA
		first := forge.Status{State: forge.Queued}
		last := forge.Status{State: forge.Blocked, Reason: "untrusted", Summary: "Approval required"}
		must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, head, first))
		must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, otherHead, first))
		for range 2 {
			must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, head, last))
		}
		if got := e.Statuses(pr); !reflect.DeepEqual(got, map[string]forge.Status{head: last, otherHead: first}) {
			t.Fatalf("statuses crossed commit boundaries: %v", got)
		}
		must(t, e.Driver.ReportStatus(ctx, e.Repo, e.PRs[1].Number, otherHead, first))
		if len(e.Statuses(pr)) != 2 || len(e.Statuses(e.PRs[1].Number)) != 1 {
			t.Fatal("status upsert crossed PR boundaries")
		}
	})
	t.Run("reads", func(t *testing.T) {
		e := newEnv(t)
		for _, want := range e.Observations {
			pr, err := e.Driver.PullRequest(ctx, e.Repo, want.Number)
			must(t, err)
			if !reflect.DeepEqual(pr, want) {
				t.Fatalf("PR observation = %+v, want open %v", pr, want)
			}
		}
		want := []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Failure}, {ID: 2, Name: "ci", State: forge.Pending}, {ID: 1, Name: "lint", State: forge.Success}}
		e.SetCheckRuns(e.PRs[0].HeadSHA, want)
		got, err := e.Driver.CheckRuns(ctx, e.Repo, e.PRs[0].HeadSHA)
		must(t, err)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("CheckRuns = %v, want %v", got, want)
		}
	})
	t.Run("ensure-fence", func(t *testing.T) {
		e := newEnv(t)
		ref := "openmerge/fence/bootstrap"
		type result struct {
			sha string
			err error
		}
		results := make(chan result, 32)
		var wg sync.WaitGroup
		for range 32 {
			wg.Add(1)
			go func() { defer wg.Done(); sha, err := e.Git.EnsureFence(ctx, e.Repo, ref); results <- result{sha, err} }()
		}
		wg.Wait()
		close(results)
		current := read(t, e, ref)
		for result := range results {
			must(t, result.err)
			if result.sha == "" || result.sha != current {
				t.Fatal("concurrent bootstrap replaced the fence")
			}
		}
		must(t, e.SetRef(ref, e.BaseSHA))
		sha, err := e.Git.EnsureFence(ctx, e.Repo, ref)
		must(t, err)
		if sha != e.BaseSHA {
			t.Fatal("EnsureFence overwrote existing fence")
		}
	})
	t.Run("concurrent-land-vs-abandon", func(t *testing.T) {
		e := newEnv(t)
		tested := build(t, e, e.BaseSHA, "atomic", e.PRs[0])
		for i := range 64 {
			fence := read(t, e, e.FenceRef)
			// Keep each iteration's base distinct from the target so partial updates
			// are observable even after an earlier landing won.
			must(t, e.SetRef(e.Base, e.BaseSHA))
			base := e.BaseSHA
			id := fmt.Sprintf("atomic-%d", i)
			type result struct {
				land bool
				sha  string
				err  error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			go func() {
				<-start
				sha, err := e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: base, TestedSHA: tested, Fence: e.FenceRef, ExpectedFence: fence, NextFenceID: "land/" + id, PRs: e.PRs[:1]})
				results <- result{true, sha, err}
			}()
			go func() {
				<-start
				sha, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, fence, "abandon/"+id)
				results <- result{false, sha, err}
			}()
			close(start)
			wins := 0
			for range 2 {
				result := <-results
				if result.err != nil {
					if !errors.Is(result.err, forge.ErrLeaseRejected) {
						t.Fatal(result.err)
					}
					continue
				}
				wins++
				if read(t, e, e.FenceRef) != result.sha {
					t.Fatal("winner's fence was lost")
				}
				wantBase := base
				if result.land {
					wantBase = tested
				}
				if read(t, e, e.Base) != wantBase {
					t.Fatal("base moved without its fence, or abandoned landing moved base")
				}
				sha, fenceID, testedSHA, err := e.Git.ReadFence(ctx, e.Repo, e.FenceRef)
				must(t, err)
				wantID := "abandon/" + id
				if result.land {
					wantID = "land/" + id
					if testedSHA != tested {
						t.Fatal("landing fence lost tested SHA")
					}
				}
				if sha != result.sha || fenceID != wantID {
					t.Fatal("atomic winner identity was lost")
				}
			}
			if wins != 1 {
				t.Fatalf("%d concurrent writes won, want one", wins)
			}
		}
	})
	for _, method := range []forge.MergeMethod{forge.Squash, forge.Rebase} {
		t.Run("mark-landed/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			sha, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: "openmerge/queue/contract/close", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "close"})
			must(t, err)
			_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: sha, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/close", PRs: e.PRs})
			must(t, err)
			for _, pr := range e.PRs {
				for range 2 {
					must(t, e.Driver.MarkLanded(ctx, e.Repo, pr.Number, sha))
				}
				got, err := e.Driver.PullRequest(ctx, e.Repo, pr.Number)
				must(t, err)
				if !got.Closed {
					t.Fatal("MarkLanded left the PR open")
				}
				if !e.Landed(pr.Number, sha) {
					t.Fatal("MarkLanded did not converge to native merged evidence or one closing commit link")
				}
			}
		})
	}

}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(fmt.Errorf("forge contract: %w", err))
	}
}
