package fake_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

var repo = forge.RepoRef{Owner: "owner", Name: "repo"}

const fenceRef = "openmerge/fence/main"

func fixture(t *testing.T) (*fake.Forge, forgetest.Env) {
	t.Helper()
	f := fake.New()
	base, fence := f.Init(repo, "main", fenceRef)
	e := forgetest.Env{Driver: f, Git: f, Repo: repo, Base: "main", FenceRef: fenceRef, BaseSHA: base, FenceSHA: fence}
	for _, n := range []int{1, 2, 3} {
		pr, err := f.OpenPR(repo, n, base, true)
		must(t, err)
		e.PRs = append(e.PRs, forge.BuildPR{Number: n, HeadSHA: pr.HeadSHA})
	}
	e.SetRef = func(ref, sha string) error { return f.SetRef(repo, ref, sha) }
	e.SetConflict = f.SetConflict
	e.SetCheckRuns = func(sha string, checks []forge.CheckRun) { f.SetCheckRuns(repo, sha, checks) }
	e.Statuses = func(pr int) map[string]forge.Status { return f.Statuses(repo, pr) }
	e.Comments = func(pr int) map[string]string { return f.Comments(repo, pr) }
	return f, e
}

func TestContract(t *testing.T) {
	forgetest.Run(t, func(t *testing.T) forgetest.Env { _, e := fixture(t); return e })
}

func TestReadFenceMessages(t *testing.T) {
	f, _ := fixture(t)
	for _, tc := range []struct{ message, id string }{
		{message: "ordinary commit"},
		{message: "prefix openmerge fence land/attempt"},
		{message: "openmerge fences land/attempt"},
		{message: "openmerge fence "},
		{message: "openmerge fence land/attempt", id: "land/attempt"},
		{message: "openmerge fence abandon/attempt", id: "abandon/attempt"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			pr, err := f.PushPR(repo, 1, tc.message)
			must(t, err)
			must(t, f.SetRef(repo, fenceRef, pr.HeadSHA))
			sha, id, err := f.ReadFence(context.Background(), repo, fenceRef)
			must(t, err)
			if sha != pr.HeadSHA || id != tc.id {
				t.Fatalf("ReadFence = (%q, %q), want (%q, %q)", sha, id, pr.HeadSHA, tc.id)
			}
		})
	}
}

func TestReadFenceErrors(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	injected := errors.New("read unavailable")
	f.FailNext("ReadFence", injected)
	if sha, id, err := f.ReadFence(ctx, repo, fenceRef); !errors.Is(err, injected) || sha != "" || id != "" {
		t.Fatalf("injected read failure = (%q, %q, %v)", sha, id, err)
	}
	sha, id, err := f.ReadFence(ctx, repo, fenceRef)
	must(t, err)
	if sha != e.FenceSHA || id != "" {
		t.Fatalf("failed read changed the fence: SHA %q ID %q", sha, id)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if sha, id, err := f.ReadFence(canceled, repo, fenceRef); !errors.Is(err, context.Canceled) || sha != "" || id != "" {
		t.Fatalf("canceled read = (%q, %q, %v)", sha, id, err)
	}
}

func TestCommitGraph(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	base, err := f.BuildTestRef(ctx, repo, forge.BuildRequest{
		Ref: "prior", BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "prior",
	})
	must(t, err)
	shas := make(map[string]bool)
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			req := forge.BuildRequest{Ref: string(method), BaseSHA: base, PRs: e.PRs[1:], Method: method, BatchID: "batch"}
			sha, err := f.BuildTestRef(ctx, repo, req)
			must(t, err)
			if shas[sha] {
				t.Fatal("different merge methods produced the same SHA")
			}
			shas[sha] = true
			again, err := f.BuildTestRef(ctx, repo, req)
			must(t, err)
			if sha != again {
				t.Fatal("identical build inputs were not deterministic")
			}
			c, err := f.Commit(repo, sha)
			must(t, err)
			if !slices.Equal(c.PRs, []int{1, 2, 3}) || !strings.Contains(c.Message, "OpenMerge-Batch: batch") {
				t.Fatalf("wrong content or missing batch ID: %+v", c)
			}
			parents, length := 1, 4
			if method == forge.Merge {
				parents = 2
			}
			if method == forge.Squash {
				length = 3
			}
			history, err := f.History(repo, req.Ref)
			must(t, err)
			if len(c.Parents) != parents || len(history) != length || history[1].SHA != base {
				t.Fatalf("wrong %s shape: parents %v, history %v", method, c.Parents, history)
			}
		})
	}
	f.SetConflict(1, 2)
	_, err = f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "blocked", BaseSHA: base, PRs: e.PRs[1:2], Method: forge.Merge, BatchID: "conflict"})
	if !errors.Is(err, forge.ErrConflict) {
		t.Fatalf("base-content conflict = %v", err)
	}
}

func TestPRLifecycleAndSnapshots(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	approval := forge.Approval{User: "maintainer", SHA: e.PRs[0].HeadSHA, CanWrite: true}
	must(t, f.Approve(repo, 1, approval))
	pushed, err := f.PushPR(repo, 1, "new head")
	must(t, err)
	if pushed.HeadSHA == approval.SHA || !reflect.DeepEqual(pushed.Approvals, []forge.Approval{approval}) {
		t.Fatal("push did not leave the old approval pinned to its exact SHA")
	}
	pushed.Approvals[0].SHA = "mutated"
	pr, err := f.PullRequest(ctx, repo, 1)
	must(t, err)
	if pr.Approvals[0] != approval {
		t.Fatal("PushPR exposed mutable approval state")
	}
	pr.Approvals[0].User = "mutated"
	must(t, f.ClosePR(repo, 1))
	closed, err := f.PullRequest(ctx, repo, 1)
	must(t, err)
	if !closed.Closed || closed.HeadSHA != pushed.HeadSHA || closed.Approvals[0] != approval {
		t.Fatalf("unexpected closed PR: %+v", closed)
	}
	fork, err := f.OpenPR(repo, 4, e.BaseSHA, false)
	must(t, err)
	if fork.HeadInBaseRepo {
		t.Fatal("fork head reported as in base repository")
	}
	// Building the old pinned head still works after a push and close.
	sha, err := f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "old", BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "old"})
	must(t, err)
	c, err := f.Commit(repo, sha)
	must(t, err)
	if c.Parents[1] != approval.SHA {
		t.Fatal("build substituted the current head for the pinned head")
	}
	c.Parents[0], c.PRs[0] = "mutated", 999
	again, err := f.Commit(repo, sha)
	must(t, err)
	if again.Parents[0] != e.BaseSHA || !slices.Equal(again.PRs, []int{1}) {
		t.Fatal("Commit exposed mutable graph state")
	}
	history, err := f.History(repo, "old")
	must(t, err)
	history[1].PRs[0] = 999
	again, err = f.Commit(repo, sha)
	must(t, err)
	if !slices.Equal(again.PRs, []int{1}) {
		t.Fatal("History exposed mutable graph state")
	}
}

func TestHookAndFenceCommits(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	calls := 0
	f.OnTestRef(func(sha string, prs []int) {
		calls++
		if !slices.Equal(prs, []int{1, 2, 3}) {
			t.Errorf("hook PRs = %v", prs)
		}
		f.SetCheckRuns(repo, sha, []forge.CheckRun{{Name: "ci", State: forge.Success}})
	})
	req := forge.BuildRequest{Ref: "tested", BaseSHA: e.BaseSHA, PRs: e.PRs, Method: forge.Merge, BatchID: "batch"}
	sha, err := f.BuildTestRef(ctx, repo, req)
	must(t, err)
	_, err = f.BuildTestRef(ctx, repo, req)
	must(t, err)
	if calls != 1 {
		t.Fatalf("idempotent build scheduled CI %d times", calls)
	}
	checks, err := f.CheckRuns(ctx, repo, sha)
	must(t, err)
	checks[0].State = forge.Failure
	again, err := f.CheckRuns(ctx, repo, sha)
	must(t, err)
	if again[0].State != forge.Success {
		t.Fatal("CheckRuns exposed mutable check state")
	}
	next, err := f.Land(ctx, repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: e.BaseSHA, TestedSHA: sha, Fence: fenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land"})
	must(t, err)
	c, err := f.Commit(repo, next)
	must(t, err)
	if !slices.Equal(c.Parents, []string{e.FenceSHA}) || c.Message != "openmerge fence land" {
		t.Fatalf("fence token is not the expected child commit: %+v", c)
	}
	next2, err := f.AdvanceFence(ctx, repo, fenceRef, next, "resolve")
	must(t, err)
	c, err = f.Commit(repo, next2)
	must(t, err)
	if !slices.Equal(c.Parents, []string{next}) || c.Message != "openmerge fence resolve" {
		t.Fatalf("advance did not mint a child fence commit: %+v", c)
	}
}

func TestConcurrentWritesAndLeaseRace(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			must(t, f.Comment(ctx, repo, 1, "same-key", "body"))
			must(t, f.ReportStatus(ctx, repo, 1, forge.Status{Context: fmt.Sprint(i), State: "queued"}))
			f.SetCheckRuns(repo, e.PRs[0].HeadSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}})
			_, err := f.CheckRuns(ctx, repo, e.PRs[0].HeadSHA)
			must(t, err)
			_, err = f.PullRequest(ctx, repo, 1)
			must(t, err)
		})
	}
	wg.Wait()
	if len(f.Comments(repo, 1)) != 1 || len(f.Statuses(repo, 1)) != 16 {
		t.Fatal("concurrent API writes lost or duplicated effects")
	}
	sha, err := f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "race", BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "race"})
	must(t, err)
	results := make(chan error, 2)
	wg.Go(func() {
		_, err := f.Land(ctx, repo, forge.LandRequest{Base: "main", ExpectedBaseSHA: e.BaseSHA, TestedSHA: sha, Fence: fenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land"})
		results <- err
	})
	wg.Go(func() {
		_, err := f.AdvanceFence(ctx, repo, fenceRef, e.FenceSHA, "resolve")
		results <- err
	})
	wg.Wait()
	first, second := <-results, <-results
	var rejected *forge.LandRejectedError
	firstWon := first == nil && errors.As(second, &rejected)
	secondWon := second == nil && errors.As(first, &rejected)
	if !firstWon && !secondWon {
		t.Fatalf("both leased writes won or unexpected failure: %v, %v", first, second)
	}
	base, err := f.ReadRef(ctx, repo, "main")
	must(t, err)
	fence, err := f.ReadRef(ctx, repo, fenceRef)
	must(t, err)
	c, err := f.Commit(repo, fence)
	must(t, err)
	if (base == sha) != (c.Message == "openmerge fence land") {
		t.Fatal("base and fence were not updated atomically")
	}
}

func TestRepositoryIsolation(t *testing.T) {
	f, e := fixture(t)
	other := forge.RepoRef{Owner: repo.Owner, Name: "other"}
	f.Init(other, "main", fenceRef)
	ctx := context.Background()
	must(t, f.ReportStatus(ctx, repo, 1, forge.Status{Context: "queue", State: "merged"}))
	must(t, f.Comment(ctx, repo, 1, "key", "body"))
	f.SetCheckRuns(repo, e.PRs[0].HeadSHA, []forge.CheckRun{{Name: "ci", State: forge.Success}})
	checks, err := f.CheckRuns(ctx, other, e.PRs[0].HeadSHA)
	must(t, err)
	if len(checks) != 0 || len(f.Statuses(other, 1)) != 0 || len(f.Comments(other, 1)) != 0 {
		t.Fatal("observations leaked between repositories")
	}
	if _, err := f.PullRequest(ctx, other, 1); err == nil {
		t.Fatal("PR leaked between repositories")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
