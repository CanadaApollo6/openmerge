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
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

var repo = forge.RepoRef{Path: "group/subgroup/repo"}

const fenceRef = "openmerge/fence/main"

func fixture(t *testing.T) (*fake.Forge, forgetest.Env) {
	t.Helper()
	f := fake.New()
	base, fence := f.Init(repo, "main", fenceRef)
	e := forgetest.Env{Driver: f, Git: f, Repo: repo, Base: "main", FenceRef: fenceRef, BaseSHA: base, FenceSHA: fence}
	e.ReadCommitMessage = func(sha string) (string, error) {
		c, err := f.Commit(repo, sha)
		return c.Message, err
	}
	for _, n := range []int{1, 2, 3} {
		pr, err := f.OpenPR(repo, n, base, n != 1)
		must(t, err)
		e.PRs = append(e.PRs, forge.BuildPR{Number: n, HeadSHA: pr.HeadSHA})
	}
	var baseID int
	e.NewBase = func(t *testing.T) string {
		t.Helper()
		baseID++
		ref := fmt.Sprintf("contract-%d", baseID)
		must(t, f.PushRef(repo, ref, base))
		return ref
	}
	e.PushBase = func(ref, sha string) error { return f.PushRef(repo, ref, sha) }
	e.CanRollbackBase = true
	e.RollbackBase = func(ref, sha string) error { return f.SetRef(repo, ref, sha) }
	e.PushPR = func(number int) string {
		pr, err := f.PushPR(repo, number, "contract push")
		must(t, err)
		return pr.HeadSHA
	}
	e.CommitCount = func(base, head string) int {
		count, err := f.CommitCount(repo, base, head)
		must(t, err)
		return count
	}
	for i, pr := range e.PRs {
		observed := forge.PullRequest{Number: pr.Number, HeadSHA: pr.HeadSHA, Base: "main", Author: "contributor", HeadInBaseRepo: i != 0}
		if i < 2 {
			user := "maintainer"
			if i == 1 {
				user = "reader"
			}
			approval := forge.Approval{User: user, SHA: pr.HeadSHA, CanWrite: i == 0}
			must(t, f.Approve(repo, pr.Number, approval))
			observed.Approvals = []forge.Approval{approval}
		}
		e.Observations = append(e.Observations, observed)
	}
	for _, n := range []int{10, 11} {
		pr, err := f.OpenPRWithFiles(repo, n, base, true, map[string]string{"conflict.txt": fmt.Sprint(n)})
		must(t, err)
		e.ConflictingPRs = append(e.ConflictingPRs, forge.BuildPR{Number: n, HeadSHA: pr.HeadSHA})
	}
	e.SetCanWrite = func(user string, write bool) { f.SetCanWrite(repo, user, write) }
	e.ContainsPR = func(sha string, pr int) bool { return f.ContainsPR(repo, sha, pr) }
	e.AddForeignComment = func(pr int, body string) { f.AddComment(repo, pr, "human", body) }
	checks := make(map[string][]forge.CheckRun)
	var checkID uint64 = 100
	e.AddCheckRun = func(sha, name string, state forge.CheckState) {
		checkID += 17
		checks[sha] = append(checks[sha], forge.CheckRun{ID: checkID, Name: name, State: state})
		f.SetCheckRuns(repo, sha, checks[sha])
	}
	e.Statuses = func(pr int) map[string][]forge.Status {
		statuses := make(map[string][]forge.Status)
		for head, status := range f.Statuses(repo, pr) {
			statuses[head] = []forge.Status{status}
		}
		return statuses
	}
	e.Comments = func(pr int) map[string]string { return f.Comments(repo, pr) }
	e.Landed = func(pr int, sha string) bool {
		comments := f.Comments(repo, pr)
		return len(comments) == 1 && strings.Contains(comments["landed/"+sha], "commit/"+sha)
	}
	return f, e
}

func TestContract(t *testing.T) {
	forgetest.Run(t, func(t *testing.T) forgetest.Env {
		_, e := fixture(t)
		e.Base = e.NewBase(t)
		return e
	})
}

func TestContractWithoutRollbacks(t *testing.T) {
	forgetest.Run(t, func(t *testing.T) forgetest.Env {
		_, e := fixture(t)
		e.Base = e.NewBase(t)
		e.CanRollbackBase = false
		e.RollbackBase = nil
		return e
	})
}

func TestReadFenceMessages(t *testing.T) {
	f, _ := fixture(t)
	const nonce = "0123456789abcdef0123456789abcdef"
	sha40, sha64 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	for _, tc := range []struct{ message, id, tested string }{
		{message: "ordinary commit"},
		{message: "prefix openmerge fence land/attempt"},
		{message: "openmerge fences land/attempt"},
		{message: "openmerge fence "},
		{message: "openmerge fence land/attempt"},
		{message: "openmerge fence abandon/attempt"},
		{message: "openmerge fence abandon/attempt nonce=0123456789abcdef0123456789abcdef", id: "abandon/attempt"},
		{message: "openmerge fence land/attempt tested=sha nonce=0123456789abcdef0123456789abcdef"},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce=" + nonce, id: "land/probe", tested: sha40},
		{message: "openmerge fence land/probe tested=" + sha64 + " nonce=" + nonce, id: "land/probe", tested: sha64},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce=not-hex tested=" + sha64},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce="},
		{message: "openmerge fence land/probe nonce=abcd tested=" + sha40},
		{message: "openmerge fence abandon/probe nonce=abcd\nopenmerge fence land/other tested=" + sha40},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce=" + nonce + " tested=" + sha64},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce=" + nonce + " nonce=" + nonce},
		{message: "openmerge fence land/probe tested=" + sha40 + " nonce=" + nonce + " extra"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			pr, err := f.PushPR(repo, 1, tc.message)
			must(t, err)
			must(t, f.SetRef(repo, fenceRef, pr.HeadSHA))
			sha, id, tested, err := f.ReadFence(context.Background(), repo, fenceRef)
			must(t, err)
			if sha != pr.HeadSHA || id != tc.id || tested != tc.tested {
				t.Fatalf("ReadFence = (%q, %q, %q), want (%q, %q, %q)", sha, id, tested, pr.HeadSHA, tc.id, tc.tested)
			}
		})
	}
}

func TestReadFenceErrors(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	injected := errors.New("read unavailable")
	f.FailNext("ReadFence", injected)
	if sha, id, _, err := f.ReadFence(ctx, repo, fenceRef); !errors.Is(err, injected) || sha != "" || id != "" {
		t.Fatalf("injected read failure = (%q, %q, %v)", sha, id, err)
	}
	sha, id, _, err := f.ReadFence(ctx, repo, fenceRef)
	must(t, err)
	if sha != e.FenceSHA || id != "" {
		t.Fatalf("failed read changed the fence: SHA %q ID %q", sha, id)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if sha, id, _, err := f.ReadFence(canceled, repo, fenceRef); !errors.Is(err, context.Canceled) || sha != "" || id != "" {
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
			history, err := f.History(repo, req.Ref)
			must(t, err)
			if len(c.Parents) != parents || len(history) != length || history[1].SHA != base {
				t.Fatalf("wrong %s shape: parents %v, history %v", method, c.Parents, history)
			}
		})
	}
	conflictBase, err := f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "conflict-base", BaseSHA: e.BaseSHA, PRs: e.ConflictingPRs[:1], Method: forge.Merge, BatchID: "conflict-base"})
	must(t, err)
	_, err = f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "blocked", BaseSHA: conflictBase, PRs: e.ConflictingPRs[1:], Method: forge.Merge, BatchID: "conflict"})
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
	wantApproval := approval
	wantApproval.CanWrite = false
	if pr.Approvals[0] != wantApproval {
		t.Fatal("PushPR exposed mutable approval state")
	}
	pr.Approvals[0].User = "mutated"
	must(t, f.ClosePR(repo, 1))
	closed, err := f.PullRequest(ctx, repo, 1)
	must(t, err)
	if !closed.Closed || closed.HeadSHA != pushed.HeadSHA || len(closed.Approvals) != 0 {
		t.Fatalf("unexpected closed PR: %+v", closed)
	}
	fork, err := f.OpenPR(repo, 4, e.BaseSHA, false)
	must(t, err)
	if fork.HeadInBaseRepo {
		t.Fatal("fork head reported as in base repository")
	}
	// Stale and mismatched PR heads must never build a test ref.
	_, err = f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "old", BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: forge.Merge, BatchID: "old"})
	if err == nil {
		t.Fatal("build accepted stale PR head")
	}
	sha, err := f.BuildTestRef(ctx, repo, forge.BuildRequest{Ref: "old", BaseSHA: e.BaseSHA, PRs: []forge.BuildPR{{Number: 1, HeadSHA: pushed.HeadSHA}}, Method: forge.Merge, BatchID: "current"})
	must(t, err)
	c, err := f.Commit(repo, sha)
	must(t, err)
	if c.Parents[1] != pushed.HeadSHA {
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
	if !slices.Equal(c.Parents, []string{e.FenceSHA}) || !strings.HasPrefix(c.Message, "openmerge fence land tested="+sha+" nonce=") {
		t.Fatalf("fence token is not the expected child commit: %+v", c)
	}
	next2, err := f.AdvanceFence(ctx, repo, fenceRef, next, "resolve")
	must(t, err)
	c, err = f.Commit(repo, next2)
	must(t, err)
	if !slices.Equal(c.Parents, []string{next}) || !strings.HasPrefix(c.Message, "openmerge fence resolve nonce=") {
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
			must(t, f.ReportStatus(ctx, repo, 1, fmt.Sprint(i), forge.Status{State: forge.Queued}))
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
	firstWon := first == nil && errors.Is(second, forge.ErrLeaseRejected)
	secondWon := second == nil && errors.Is(first, forge.ErrLeaseRejected)
	if !firstWon && !secondWon {
		t.Fatalf("both leased writes won or unexpected failure: %v, %v", first, second)
	}
	base, err := f.ReadRef(ctx, repo, "main")
	must(t, err)
	fence, err := f.ReadRef(ctx, repo, fenceRef)
	must(t, err)
	c, err := f.Commit(repo, fence)
	must(t, err)
	if (base == sha) != strings.HasPrefix(c.Message, "openmerge fence land tested="+sha+" nonce=") {
		t.Fatal("base and fence were not updated atomically")
	}
}

func TestRepositoryIsolation(t *testing.T) {
	f, e := fixture(t)
	other := forge.RepoRef{Path: "group/subgroup/other"}
	f.Init(other, "main", fenceRef)
	ctx := context.Background()
	must(t, f.ReportStatus(ctx, repo, 1, e.PRs[0].HeadSHA, forge.Status{State: forge.Merged}))
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

func TestReconciliationMetadataAndCurrentPermissions(t *testing.T) {
	f, e := fixture(t)
	ctx := context.Background()
	must(t, f.SetPRMetadata(repo, 1, "release/v1", true, "author"))
	pr, err := f.PullRequest(ctx, repo, 1)
	must(t, err)
	if pr.Base != "release/v1" || !pr.Draft || pr.Author != "author" {
		t.Fatalf("metadata lost: %+v", pr)
	}
	summaries, err := f.ListOpenPullRequests(ctx, repo, "release/v1")
	must(t, err)
	if len(summaries) != 1 || summaries[0].Number != 1 || summaries[0].HeadSHA != pr.HeadSHA || summaries[0].UpdatedAt.IsZero() {
		t.Fatalf("incorrect reconciliation view: %+v", summaries)
	}
	f.SetCanWrite(repo, "maintainer", false)
	allowed, err := f.CanWrite(ctx, repo, "maintainer")
	must(t, err)
	if allowed {
		t.Fatal("revoked user retained command permission")
	}
	pr, err = f.PullRequest(ctx, repo, 1)
	must(t, err)
	if pr.Approvals[0].CanWrite {
		t.Fatal("revoked user retained approval permission")
	}
	f.SetCanWrite(repo, "maintainer", true)
	pr, err = f.PullRequest(ctx, repo, 1)
	must(t, err)
	if !pr.Approvals[0].CanWrite {
		t.Fatal("permission restoration was not observed")
	}
	f.AddComment(repo, 1, "author", "@openmerge enqueue")
	comments, err := f.ListComments(ctx, repo, 1, time.Time{})
	must(t, err)
	if len(comments) != 1 || comments[0].ID <= 0 || comments[0].Author != "author" || comments[0].CreatedAt.IsZero() {
		t.Fatalf("incomplete command: %+v", comments)
	}
	later, err := f.ListComments(ctx, repo, 1, comments[0].CreatedAt)
	must(t, err)
	if len(later) != 1 || later[0].ID != comments[0].ID {
		t.Fatal("inclusive cursor omitted its boundary comment")
	}
	must(t, f.ClosePR(repo, 1))
	summaries, err = f.ListOpenPullRequests(ctx, repo, "release/v1")
	must(t, err)
	if len(summaries) != 0 {
		t.Fatal("closed PR remained in reconciliation")
	}
	checks := []forge.CheckRun{{ID: 1, Name: "CI", Source: "app:11", State: forge.Failure, HeadBranch: "contributor/reposted-marker"}, {ID: 2, Name: "CI", Source: "app:22", State: forge.Success, HeadBranch: "other"}}
	f.SetCheckRuns(repo, e.PRs[0].HeadSHA, checks)
	got, err := f.CheckRuns(ctx, repo, e.PRs[0].HeadSHA)
	must(t, err)
	if !reflect.DeepEqual(got, checks) {
		t.Fatalf("producer identities lost: %+v", got)
	}
}
