// Package forgetest provides the contract suite shared by forge implementations.
package forgetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Env supplies an isolated repository with a base root, a fence root and at least
// two open PR heads. Hooks perform sandbox setup and inspect API write effects;
// they may close over t to report setup/inspection failures on real forges.
type Env struct {
	Driver                            forge.Driver
	Git                               forge.Git
	Repo                              forge.RepoRef
	Base, FenceRef, BaseSHA, FenceSHA string
	PRs                               []forge.BuildPR
	SetRef                            func(ref, sha string) error
	SetConflict                       func(prA, prB int)
	SetCheckRuns                      func(sha string, checks []forge.CheckRun)
	Statuses                          func(pr int) map[string]forge.Status
	Comments                          func(pr int) map[string]string
}

// Run checks observable contracts. Every subtest receives a fresh environment.
// Repeated API writes model a crash after the effect but before its recording.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	t.Helper()
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
			sha, id, err := e.Git.ReadFence(ctx, e.Repo, e.FenceRef)
			must(t, err)
			if sha != wantSHA || id != wantID {
				t.Fatalf("ReadFence = (%q, %q), want (%q, %q)", sha, id, wantSHA, wantID)
			}
		}
	}
	t.Run("read-fence", func(t *testing.T) {
		e := newEnv(t)
		must(t, e.SetRef(e.FenceRef, e.BaseSHA))
		readFence(t, e, e.BaseSHA, "")
		if _, _, err := e.Git.ReadFence(ctx, e.Repo, "openmerge/fence/missing"); err == nil {
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
			var rejected *forge.LandRejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("Land error = %v, want lease rejection", err)
			}
			if rejected.ActualBaseSHA != beforeBase || rejected.ActualFence != beforeFence {
				t.Fatalf("rejection = %+v, want base %s fence %s", rejected, beforeBase, beforeFence)
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
		var rejected *forge.LandRejectedError
		if !errors.As(err, &rejected) || rejected.ActualFence != next {
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
		if !errors.As(err, &rejected) || read(t, e, e.Base) != e.BaseSHA || read(t, e, e.FenceRef) != next {
			t.Fatalf("fenced delayed landing was not refused atomically: %v", err)
		}
	})
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run("unique-test-commit/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			req := forge.BuildRequest{Ref: "openmerge/queue/contract/unique", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "first"}
			first, err := e.Git.BuildTestRef(ctx, e.Repo, req)
			must(t, err)
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
		e.SetConflict(e.PRs[0].Number, e.PRs[1].Number)
		_, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
			Ref: "openmerge/queue/contract/conflict", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: forge.Merge, BatchID: "conflict",
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
		first := forge.Status{Context: "queue", State: "queued"}
		other := forge.Status{Context: "other", State: "pending"}
		last := forge.Status{Context: "queue", State: "merged", Detail: "landed"}
		for _, status := range []forge.Status{first, other, last, last} {
			must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, status))
		}
		if got := e.Statuses(pr); !reflect.DeepEqual(got, map[string]forge.Status{"queue": last, "other": other}) {
			t.Fatalf("statuses = %v, want one latest status per context", got)
		}
		must(t, e.Driver.ReportStatus(ctx, e.Repo, e.PRs[1].Number, first))
		if len(e.Statuses(pr)) != 2 || len(e.Statuses(e.PRs[1].Number)) != 1 {
			t.Fatal("status upsert crossed PR boundaries")
		}
	})
	t.Run("reads", func(t *testing.T) {
		e := newEnv(t)
		for _, want := range e.PRs {
			pr, err := e.Driver.PullRequest(ctx, e.Repo, want.Number)
			must(t, err)
			if pr.Number != want.Number || pr.HeadSHA != want.HeadSHA || pr.Closed {
				t.Fatalf("PR observation = %+v, want open %v", pr, want)
			}
		}
		want := []forge.CheckRun{{Name: "ci", State: forge.Pending}, {Name: "lint", State: forge.Success}}
		e.SetCheckRuns(e.PRs[0].HeadSHA, want)
		got, err := e.Driver.CheckRuns(ctx, e.Repo, e.PRs[0].HeadSHA)
		must(t, err)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("CheckRuns = %v, want %v", got, want)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(fmt.Errorf("forge contract: %w", err))
	}
}
