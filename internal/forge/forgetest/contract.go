// Package forgetest provides the contract suite shared by forge implementations.
package forgetest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Env supplies a sandbox with at least two PRs based on BaseSHA adding unique
// files, and a separate pair adding conflicting file content. Observations are
// independent fixture expectations, including approved fork and same-repository
// PRs and both writable and non-writable reviewers.
// The factory creates Base with NewBase before opening PRs targeted at it.
// Hooks perform ordinary sandbox setup and inspect API write effects;
// they may close over t to report setup/inspection failures on real forges.
type Env struct {
	Driver forge.Driver
	// RefExists is required by RunDriver when Git is nil.
	RefExists                         func(ref string) bool
	Git                               forge.Git
	Repo                              forge.RepoRef
	Base, FenceRef, BaseSHA, FenceSHA string
	PRs                               []forge.BuildPR
	// NewBase creates a fresh, uniquely named branch at BaseSHA by ordinary push.
	// Run derives its fence name from that branch and initializes it with EnsureFence.
	// The factory must clean up its branches, fence refs, test refs, and PRs.
	NewBase func(t *testing.T) string
	// PushBase permits only ordinary fast-forward updates.
	PushBase func(base, sha string) error
	// CanRollbackBase enables the optional rollback scenario. RollbackBase must
	// only rewrite fresh unprotected test branches, never a protected base/fence.
	CanRollbackBase bool
	RollbackBase    func(base, sha string) error
	// PushPR adds one commit with a unique file and returns the new actual head.
	PushPR func(pr int) string
	// CommitCount inspects the git graph, equivalent to git rev-list --count base..head.
	CommitCount func(base, head string) int
	// ApprovalsResetOnPush requires old approvals to disappear after a push.
	// Otherwise approvals must remain attributed to the previously approved SHA.
	ApprovalsResetOnPush bool
	ConflictingPRs       []forge.BuildPR
	Observations         []forge.PullRequest
	ContainsPR           func(sha string, pr int) bool
	// ReadCommitMessage returns the complete message for a commit. Git does not
	// expose this through the core Git contract, but metadata contracts need a
	// read-only fixture hook for both the fake and CLI-backed implementations.
	ReadCommitMessage func(sha string) (string, error)
	AddForeignComment func(pr int, body string)
	// SetCanWrite changes current permission without advancing a cache clock.
	SetCanWrite func(user string, write bool)
	// AddCheckRun creates a new run; the forge assigns its ID, even for reruns.
	AddCheckRun func(sha, name string, state forge.CheckState)
	// Statuses returns current OpenMerge checks by head, preserving duplicates.
	// Superseded commit-status history is excluded, as by a forge's combined-status API.
	Statuses func(pr int) map[string][]forge.Status
	// Comments returns only driver-authored comments, keyed by marker.
	Comments func(pr int) map[string]string
	// Landed verifies native merged evidence or exactly one closing commit link.
	Landed func(pr int, sha string) bool
}

// Run checks observable contracts. Every subtest receives a fresh environment.
// Repeated API writes model a crash after the effect but before its recording.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	runPushOptions(t, newEnv)
	run(t, newEnv, true)
}

// RunGit checks only the Git-side contracts. Driver and API fixture hooks may
// be nil; metadata subtests require ReadCommitMessage, and other Env fields
// have the same requirements as Run.
func RunGit(t *testing.T, newEnv func(t *testing.T) Env) {
	runPushOptions(t, newEnv)
	run(t, newEnv, false)
}

// RunCheckRerunner exercises the optional same-commit retry contract. The
// observer is deliberately supplied by the fixture: a successful request only
// schedules work, so the driver must expose the new pending attempt through its
// ordinary CheckRuns observation without making CI calls here.
func RunCheckRerunner(t *testing.T, repo forge.RepoRef, rerunner forge.CheckRerunner, request forge.RerunChecksRequest, observe func() ([]forge.CheckRun, error)) {
	t.Helper()
	if request.TestedSHA == "" || request.TestRef == "" || request.Key == "" || len(request.Runs) == 0 {
		t.Fatal("rerun contract needs a complete request")
	}
	before, err := observe()
	must(t, err)
	result, err := rerunner.RerunFailedChecks(context.Background(), repo, request)
	must(t, err)
	if result.Unsupported {
		t.Fatalf("capable rerunner reported unsupported: %+v", result)
	}
	afterFirst, err := observe()
	must(t, err)
	result, err = rerunner.RerunFailedChecks(context.Background(), repo, request)
	must(t, err)
	if result.Unsupported {
		t.Fatalf("idempotent rerun reported unsupported: %+v", result)
	}
	afterSecond, err := observe()
	must(t, err)
	if len(afterSecond) != len(afterFirst) {
		t.Fatalf("duplicate rerun changed observed run count: first=%d second=%d", len(afterFirst), len(afterSecond))
	}
	for _, requested := range request.Runs {
		if !hasNewPendingAttempt(before, afterFirst, requested, request.TestRef) {
			t.Fatalf("rerun did not add a pending newer attempt for %+v: before=%+v after=%+v", requested, before, afterFirst)
		}
	}
}

func hasNewPendingAttempt(before, after []forge.CheckRun, requested forge.CheckRun, testRef string) bool {
	for _, candidate := range after {
		if candidate.Name != requested.Name || candidate.Source != requested.Source || candidate.HeadBranch != testRef || candidate.State != forge.Pending {
			continue
		}
		if !checkRunNewer(candidate, requested) {
			continue
		}
		seen := false
		for _, old := range before {
			if old == candidate {
				seen = true
				break
			}
		}
		if !seen {
			return true
		}
	}
	return false
}

func checkRunNewer(candidate, current forge.CheckRun) bool {
	if candidate.ID != current.ID {
		return candidate.ID > current.ID
	}
	return attemptOrFirst(candidate.Attempt) > attemptOrFirst(current.Attempt)
}

func attemptOrFirst(attempt uint64) uint64 {
	if attempt == 0 {
		return 1
	}
	return attempt
}

func run(t *testing.T, newEnv func(t *testing.T) Env, driver bool) {
	t.Helper()
	factory := newEnv
	ctx := context.Background()
	withFence := func(t *testing.T, e Env) Env {
		t.Helper()
		e.FenceRef = "openmerge/fence/" + e.Base
		var err error
		e.FenceSHA, err = e.Git.EnsureFence(ctx, e.Repo, e.FenceRef, forge.PushOptions{})
		must(t, err)
		return e
	}
	newEnv = func(t *testing.T) Env {
		t.Helper()
		e := factory(t)
		if len(e.PRs) < 2 || len(e.ConflictingPRs) != 2 {
			t.Fatal("contract needs unique-file PRs and a conflicting pair")
		}
		if driver {
			validateObservations(t, e)
		}
		if e.Base == "" {
			t.Fatal("factory must create a fresh base before opening PRs")
		}
		base, err := e.Git.ReadRef(ctx, e.Repo, e.Base)
		must(t, err)
		if base != e.BaseSHA {
			t.Fatal("fresh base does not point to BaseSHA")
		}
		return withFence(t, e)
	}
	runGitValidation(t, newEnv)
	testRef := func(e Env, name string) string { return "openmerge/queue/" + e.Base + "/" + name }
	build := func(t *testing.T, e Env, base, batch string, prs ...forge.BuildPR) string {
		t.Helper()
		sha, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
			Ref: testRef(e, batch), BaseSHA: base,
			PRs: prs, Method: forge.Merge, BatchID: batch,
		}, forge.PushOptions{})
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
	readCommitMessages := func(t *testing.T, e Env, base, head string) []string {
		t.Helper()
		if e.ReadCommitMessage == nil {
			t.Fatal("metadata contract requires Env.ReadCommitMessage")
		}
		history, ok := e.Git.(forge.CommitHistoryReader)
		if !ok {
			t.Fatal("metadata contract requires forge.CommitHistoryReader")
		}
		commits, truncated, err := history.CommitsBetween(ctx, e.Repo, base, head, 1000)
		must(t, err)
		if truncated {
			t.Fatal("metadata contract commit history was truncated")
		}
		messages := make([]string, 0, len(commits))
		for _, sha := range commits {
			message, err := e.ReadCommitMessage(sha)
			must(t, err)
			messages = append(messages, message)
		}
		return messages
	}
	contractMetadata := func(revision uint64, labels ...string) *forge.ChangeMetadata {
		return &forge.ChangeMetadata{
			ChangeID: fmt.Sprintf("chg1_%064x", revision),
			Revision: revision,
			Labels:   labels,
		}
	}
	t.Run("commit-acquisition", func(t *testing.T) {
		e := newEnv(t)
		acquirer, ok := e.Git.(forge.CommitAcquirer)
		if !ok {
			t.Fatal("missing CommitAcquirer")
		}
		for _, pins := range [][]string{nil, {"main"}, {strings.Repeat("f", 40)}, make([]string, forge.MaxFirstParentCommits+3)} {
			release, err := acquirer.AcquireCommits(ctx, e.Repo, pins)
			if err == nil {
				release()
				t.Fatalf("accepted invalid acquisition: count=%d", len(pins))
			}
			if release != nil {
				t.Fatal("failed acquisition returned a lease")
			}
		}
		head := build(t, e, e.BaseSHA, "acquire", e.PRs[:1]...)
		release, err := acquirer.AcquireCommits(ctx, e.Repo, []string{e.BaseSHA, head})
		must(t, err)
		defer release()
		if message, err := e.ReadCommitMessage(head); err != nil || message == "" {
			t.Fatalf("acquired message: %q %v", message, err)
		}
		release()
	})
	t.Run("history-limits", func(t *testing.T) {
		e := newEnv(t)
		// Two two-member compositions produce a four-entry first-parent
		// chain, the same limit requested by a depth-four merge/squash gate.
		first := build(t, e, e.BaseSHA, "history-first", e.PRs[:2]...)
		head := build(t, e, first, "history-second", e.PRs[:2]...)
		firstParent, ok := e.Git.(forge.FirstParentCommitReader)
		if !ok {
			t.Fatal("missing FirstParentCommitReader")
		}
		history, ok := e.Git.(forge.CommitHistoryReader)
		if !ok {
			t.Fatal("missing CommitHistoryReader")
		}
		for _, reader := range []struct {
			name string
			max  int
			read func(context.Context, forge.RepoRef, string, string, int) ([]string, bool, error)
		}{
			{"first-parent", forge.MaxFirstParentCommits, firstParent.FirstParentCommits},
			{"commit", forge.MaxCommitHistoryCommits, history.CommitsBetween},
		} {
			for _, limit := range []int{-1, 0, 1, 4, reader.max, reader.max + 1} {
				t.Run(fmt.Sprintf("%s/%d", reader.name, limit), func(t *testing.T) {
					commits, truncated, err := reader.read(ctx, e.Repo, e.BaseSHA, head, limit)
					if limit < 1 || limit > reader.max {
						if !errors.Is(err, forge.ErrInvalidInput) || len(commits) != 0 || truncated {
							t.Fatalf("invalid limit: commits=%v truncated=%v err=%v", commits, truncated, err)
						}
						return
					}
					must(t, err)
					if len(commits) == 0 || len(commits) > limit || commits[0] != head {
						t.Fatalf("bounded newest-first history: %v", commits)
					}
					if reader.name == "first-parent" && (len(commits) != min(limit, 4) || truncated != (limit < 4)) {
						t.Fatalf("four-member composition: %v truncated=%v", commits, truncated)
					}
					if limit == reader.max && truncated {
						t.Fatal("small graph exhausted maximum limit")
					}
				})
			}
		}
	})
	t.Run("read-fence", func(t *testing.T) {
		e := newEnv(t)
		// Read an ordinary commit without replacing an existing fence.
		e.FenceRef = e.NewBase(t)
		readFence(t, e, e.BaseSHA, "")
		if _, _, _, err := e.Git.ReadFence(ctx, e.Repo, testRef(e, "missing")); err == nil {
			t.Fatal("ReadFence accepted a missing ref")
		}
	})
	for _, moved := range []string{"matching", "forward", "rollback", "fence"} {
		t.Run("land/"+moved, func(t *testing.T) {
			e := newEnv(t)
			if moved == "rollback" && !e.CanRollbackBase {
				t.Skip("sandbox does not allow rollbacks of fresh unprotected test branches")
			}
			current := build(t, e, e.BaseSHA, "current", e.PRs[0])
			must(t, e.PushBase(e.Base, current))
			tested := build(t, e, current, "tested", e.PRs[1])
			switch moved {
			case "forward":
				must(t, e.PushBase(e.Base, build(t, e, current, "forward", e.PRs[1])))
			case "rollback":
				must(t, e.RollbackBase(e.Base, e.BaseSHA))
			case "fence":
				_, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "other-worker", forge.PushOptions{})
				must(t, err)
			}
			beforeBase, beforeFence := read(t, e, e.Base), read(t, e, e.FenceRef)
			req := forge.LandRequest{
				Base: e.Base, ExpectedBaseSHA: current, TestedSHA: tested,
				Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/contract",
			}
			next, err := e.Git.Land(ctx, e.Repo, req, forge.PushOptions{})
			if moved == "matching" {
				must(t, err)
				if next == "" || next == e.FenceSHA || read(t, e, e.Base) != tested || read(t, e, e.FenceRef) != next {
					t.Fatal("Land did not move both refs to the tested commit and returned fence SHA")
				}
				readFence(t, e, next, req.NextFenceID)
				// An unrecorded successful push must refuse its old leases on retry.
				beforeBase, beforeFence = tested, next
				_, err = e.Git.Land(ctx, e.Repo, req, forge.PushOptions{})
			}
			if !errors.Is(err, forge.ErrLeaseRejected) {
				t.Fatalf("Land error = %v, want lease rejection", err)
			}
			if read(t, e, e.Base) != beforeBase || read(t, e, e.FenceRef) != beforeFence {
				t.Fatal("rejected Land changed a ref")
			}
		})
	}
	t.Run("land/never-leaves-fence-unchanged", func(t *testing.T) {
		e := newEnv(t)
		tested := build(t, e, e.BaseSHA, "unique-fence", e.PRs[0])
		req := forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: tested,
			Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/same-attempt", PRs: e.PRs[:1]}
		next, err := e.Git.Land(ctx, e.Repo, req, forge.PushOptions{})
		must(t, err)
		if next == e.FenceSHA || read(t, e, e.FenceRef) != next {
			t.Fatal("a landing push must never leave the fence unchanged")
		}
		if e.CanRollbackBase {
			must(t, e.RollbackBase(e.Base, e.BaseSHA))
			_, err = e.Git.Land(ctx, e.Repo, req, forge.PushOptions{})
			if !errors.Is(err, forge.ErrLeaseRejected) || read(t, e, e.Base) != e.BaseSHA || read(t, e, e.FenceRef) != next {
				t.Fatalf("duplicate landing after rollback was not refused without effects: %v", err)
			}
		}
	})
	for _, matchingLease := range []bool{false, true} {
		t.Run(fmt.Sprintf("land/already-tested/matching-lease=%v", matchingLease), func(t *testing.T) {
			e := newEnv(t)
			tested := build(t, e, e.BaseSHA, "already-tested", e.PRs[0])
			must(t, e.PushBase(e.Base, tested))
			expected := e.BaseSHA
			if matchingLease {
				expected = tested
			}
			_, err := e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: expected, TestedSHA: tested,
				Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/already-tested", PRs: e.PRs[:1]}, forge.PushOptions{})
			if !errors.Is(err, forge.ErrLeaseRejected) || read(t, e, e.Base) != tested || read(t, e, e.FenceRef) != e.FenceSHA {
				t.Fatalf("already-tested base was not refused without a push: %v", err)
			}
		})
	}
	t.Run("advance-fence", func(t *testing.T) {
		e := newEnv(t)
		next, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "abandon/contract", forge.PushOptions{})
		must(t, err)
		if next == "" || next == e.FenceSHA || read(t, e, e.FenceRef) != next || read(t, e, e.Base) != e.BaseSHA {
			t.Fatal("AdvanceFence did not return the new fence SHA while preserving base")
		}
		readFence(t, e, next, "abandon/contract")
		_, err = e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, e.FenceSHA, "stale", forge.PushOptions{})
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
		}, forge.PushOptions{})
		if !errors.Is(err, forge.ErrLeaseRejected) || read(t, e, e.Base) != e.BaseSHA || read(t, e, e.FenceRef) != next {
			t.Fatalf("fenced delayed landing was not refused atomically: %v", err)
		}
	})
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run("change-metadata/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			prs := slices.Clone(e.PRs[:2])
			prs[0].Metadata = contractMetadata(1, "owned:user:alice", "docs")
			prs[1].Metadata = contractMetadata(2)
			batch := "metadata-" + string(method)
			tested, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
				Ref: testRef(e, batch), BaseSHA: e.BaseSHA, PRs: prs, Method: method, BatchID: batch,
			}, forge.PushOptions{})
			must(t, err)
			messages := readCommitMessages(t, e, e.BaseSHA, tested)
			blocks := make([]string, len(prs))
			for i, pr := range prs {
				pair, err := pr.Metadata.Trailers()
				must(t, err)
				blocks[i] = strings.Join(pair, "\n")
			}
			for _, message := range messages {
				members := 0
				for _, block := range blocks {
					if strings.Contains(message, block) {
						members++
					}
				}
				if members > 1 {
					t.Fatalf("one generated commit retained metadata for %d changes", members)
				}
			}
			for i, pr := range prs {
				pair, err := pr.Metadata.Trailers()
				must(t, err)
				matches := 0
				for _, message := range messages {
					matches += strings.Count(message, blocks[i])
				}
				if matches != 1 {
					t.Fatalf("PR %d metadata block count = %d, want one: %q", pr.Number, matches, blocks[i])
				}
				if i == 1 && !strings.HasSuffix(pair[1], " none") {
					t.Fatalf("empty labels = %q, want none classification", pair[1])
				}
			}
		})
	}
	t.Run("change-metadata/fast-forward", func(t *testing.T) {
		e := newEnv(t)
		pr := e.PRs[0]
		pr.Metadata = contractMetadata(3, "owned:team:org/platform", "docs")
		batch := "metadata-fast-forward"
		req := forge.BuildRequest{Ref: testRef(e, batch), BaseSHA: e.BaseSHA,
			PRs: []forge.BuildPR{pr}, Method: forge.FastForward, BatchID: batch}
		first, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
		must(t, err)
		second, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
		must(t, err)
		if e.ReadCommitMessage == nil {
			t.Fatal("metadata contract requires Env.ReadCommitMessage")
		}
		pair, err := pr.Metadata.Trailers()
		must(t, err)
		want := strings.Join(append(pair, "OpenMerge-Batch: "+batch), "\n")
		var firstBlock string
		for i, sha := range []string{first, second} {
			message, err := e.ReadCommitMessage(sha)
			must(t, err)
			if strings.Count(message, "OpenMerge-Batch: ") != 1 || forge.ParseBatchMessage(message) != batch {
				t.Fatalf("marker %d batch trailer is not authoritative and last: %q", i, message)
			}
			start := strings.Index(message, pair[0])
			got := ""
			if start >= 0 {
				got = strings.TrimSpace(message[start:])
			}
			if start < 0 || got != want {
				t.Fatalf("marker %d metadata trailers = %q, want %q", i, got, want)
			}
			if i == 0 {
				firstBlock = got
			} else if got != firstBlock {
				t.Fatalf("fast-forward metadata block changed across identical builds")
			}
		}
	})
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run("build-rejects-batch-id/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			req := forge.BuildRequest{Ref: testRef(e, "batch-validation"), BaseSHA: e.BaseSHA, PRs: e.PRs[:1], Method: method, BatchID: "valid"}
			before, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
			must(t, err)
			for _, id := range []string{"", "two words", "b/1", "b\n1", "b\r1", "b\x001", "b\u0431"} {
				req.BatchID = id
				if _, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{}); err == nil {
					t.Fatalf("invalid batch ID %q accepted", id)
				}
				if got := read(t, e, req.Ref); got != before {
					t.Fatalf("invalid batch ID %q changed ref to %s", id, got)
				}
			}
		})
		t.Run("unique-test-commit/"+string(method), func(t *testing.T) {
			e := newEnv(t)
			req := forge.BuildRequest{Ref: testRef(e, "unique"), BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "first"}
			first, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
			must(t, err)
			ancestor, err := e.Git.IsAncestor(ctx, e.Repo, e.BaseSHA, first)
			must(t, err)
			if !ancestor {
				t.Fatal("base is not an ancestor of test commit")
			}
			if method == forge.Squash && e.CommitCount(e.BaseSHA, first) != len(e.PRs) {
				t.Fatal("squash did not create exactly one single-parent commit per PR")
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
			second, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
			must(t, err)
			if first == second || read(t, e, req.Ref) != second {
				t.Fatal("different batch IDs reused a top commit, or ref was not pushed")
			}
		})
	}
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		for _, mismatch := range []string{"other-pr-head", "stale-head"} {
			t.Run("build-rejects-head/"+string(method)+"/"+mismatch, func(t *testing.T) {
				e := newEnv(t)
				req := forge.BuildRequest{Ref: testRef(e, "head-validation"), BaseSHA: e.BaseSHA, PRs: slices.Clone(e.PRs), Method: method, BatchID: "valid"}
				before, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{})
				must(t, err)
				if mismatch == "stale-head" {
					e.PushPR(req.PRs[1].Number)
				} else {
					req.PRs[1].HeadSHA = req.PRs[0].HeadSHA
				}
				req.BatchID = "invalid"
				if _, err := e.Git.BuildTestRef(ctx, e.Repo, req, forge.PushOptions{}); err == nil {
					t.Fatal("BuildTestRef accepted a head that is not the PR's actual head")
				}
				if read(t, e, req.Ref) != before {
					t.Fatal("rejected build moved the test ref")
				}
			})
			t.Run("land-rejects-head/"+string(method)+"/"+mismatch, func(t *testing.T) {
				e := newEnv(t)
				tested, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: testRef(e, "head-validation"), BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "valid"}, forge.PushOptions{})
				must(t, err)
				prs := slices.Clone(e.PRs)
				if mismatch == "stale-head" {
					e.PushPR(prs[1].Number)
				} else {
					prs[1].HeadSHA = prs[0].HeadSHA
				}
				_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: tested, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/invalid-head", PRs: prs}, forge.PushOptions{})
				if err == nil {
					t.Fatal("Land accepted a head that is not the PR's actual head")
				}
				if read(t, e, e.Base) != e.BaseSHA || read(t, e, e.FenceRef) != e.FenceSHA {
					t.Fatal("rejected Land moved a ref")
				}
				if driver {
					for _, pinned := range e.PRs {
						pr, err := e.Driver.PullRequest(ctx, e.Repo, pinned.Number)
						must(t, err)
						if pr.Closed {
							t.Fatal("rejected Land closed a PR")
						}
					}
				}
			})
		}
	}
	t.Run("rebase-replays-each-commit", func(t *testing.T) {
		e := newEnv(t)
		prs := slices.Clone(e.PRs[:2])
		want := 0
		for i := range prs {
			before := e.CommitCount(e.BaseSHA, prs[i].HeadSHA)
			pushes := i + 1
			for range pushes {
				prs[i].HeadSHA = e.PushPR(prs[i].Number)
			}
			count := e.CommitCount(e.BaseSHA, prs[i].HeadSHA)
			if before < 1 || count != before+pushes {
				t.Fatalf("PushPR did not add %d distinct commits to PR %d", pushes, prs[i].Number)
			}
			want += count
		}
		tested, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: testRef(e, "replay"), BaseSHA: e.BaseSHA, PRs: prs, Method: forge.Rebase, BatchID: "replay"}, forge.PushOptions{})
		must(t, err)
		ancestor, err := e.Git.IsAncestor(ctx, e.Repo, e.BaseSHA, tested)
		must(t, err)
		if !ancestor || e.CommitCount(e.BaseSHA, tested) != want {
			t.Fatalf("rebase did not replay all %d PR commits on the base", want)
		}
		for _, pr := range prs {
			if !e.ContainsPR(tested, pr.Number) {
				t.Fatalf("rebase omitted PR %d content", pr.Number)
			}
			ancestor, err := e.Git.IsAncestor(ctx, e.Repo, pr.HeadSHA, tested)
			must(t, err)
			if ancestor {
				t.Fatalf("rebase merged PR %d instead of replaying its commits", pr.Number)
			}
		}
	})
	t.Run("conflict", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{
			Ref: testRef(e, "conflict"), BaseSHA: e.BaseSHA, PRs: e.ConflictingPRs, Method: forge.Merge, BatchID: "conflict",
		}, forge.PushOptions{})
		if !errors.Is(err, forge.ErrConflict) {
			t.Fatalf("BuildTestRef error = %v, want ErrConflict", err)
		}
	})
	if driver {
		t.Run("delete-test-ref", func(t *testing.T) {
			e := newEnv(t)
			build(t, e, e.BaseSHA, "delete", e.PRs[0])
			for range 2 {
				must(t, e.Driver.DeleteRef(ctx, e.Repo, testRef(e, "delete")))
			}
			if _, err := e.Git.ReadRef(ctx, e.Repo, testRef(e, "delete")); err == nil {
				t.Fatal("deleted test ref still exists")
			}
			must(t, e.Driver.DeleteRef(ctx, e.Repo, testRef(e, "never-existed")))
		})
		RunDriver(t, factory)
	}

	t.Run("ensure-fence", func(t *testing.T) {
		e := newEnv(t)
		ref := "openmerge/fence/" + e.NewBase(t)
		type result struct {
			sha string
			err error
		}
		results := make(chan result, 32)
		var wg sync.WaitGroup
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sha, err := e.Git.EnsureFence(ctx, e.Repo, ref, forge.PushOptions{})
				results <- result{sha, err}
			}()
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
		// Start with a separate existing ordinary ref; do not reset a fence.
		ref = e.NewBase(t)
		sha, err := e.Git.EnsureFence(ctx, e.Repo, ref, forge.PushOptions{})
		must(t, err)
		if sha != e.BaseSHA {
			t.Fatal("EnsureFence overwrote existing fence")
		}
	})
	t.Run("concurrent-land-vs-abandon", func(t *testing.T) {
		e := newEnv(t)
		tested := build(t, e, e.BaseSHA, "atomic", e.PRs[0])
		for i := range 64 {
			if i > 0 {
				e.Base = e.NewBase(t)
				e = withFence(t, e)
			}
			fence := read(t, e, e.FenceRef)
			// Each race starts on fresh refs, so partial updates remain observable
			// without rolling back a base that a prior iteration already landed.
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
				sha, err := e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: base, TestedSHA: tested, Fence: e.FenceRef, ExpectedFence: fence, NextFenceID: "land/" + id, PRs: e.PRs[:1]}, forge.PushOptions{})
				results <- result{true, sha, err}
			}()
			go func() {
				<-start
				sha, err := e.Git.AdvanceFence(ctx, e.Repo, e.FenceRef, fence, "abandon/"+id, forge.PushOptions{})
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
	if driver {
		for _, method := range []forge.MergeMethod{forge.Squash, forge.Rebase} {
			t.Run("mark-landed/"+string(method), func(t *testing.T) {
				e := newEnv(t)
				sha, err := e.Git.BuildTestRef(ctx, e.Repo, forge.BuildRequest{Ref: testRef(e, "close"), BaseSHA: e.BaseSHA, PRs: e.PRs, Method: method, BatchID: "close"}, forge.PushOptions{})
				must(t, err)
				_, err = e.Git.Land(ctx, e.Repo, forge.LandRequest{Base: e.Base, ExpectedBaseSHA: e.BaseSHA, TestedSHA: sha, Fence: e.FenceRef, ExpectedFence: e.FenceSHA, NextFenceID: "land/close", PRs: e.PRs}, forge.PushOptions{})
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

}

func assertStatuses(t *testing.T, got map[string][]forge.Status, want map[string]forge.Status) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("status heads = %v, want %v", got, want)
	}
	for head, status := range want {
		if len(got[head]) != 1 || got[head][0] != status {
			t.Fatalf("head %s statuses = %v, want one latest status %+v", head, got[head], status)
		}
	}
}

func assertPullRequest(t *testing.T, got, want forge.PullRequest) {
	t.Helper()
	if got.Number != want.Number || got.HeadSHA != want.HeadSHA || got.HeadInBaseRepo != want.HeadInBaseRepo || got.Closed != want.Closed || got.Base != want.Base || got.Draft != want.Draft || got.Author != want.Author {
		t.Fatalf("PR observation = %+v, want number %d head %s in-base-repo %v closed %v", got, want.Number, want.HeadSHA, want.HeadInBaseRepo, want.Closed)
	}
	type approval struct {
		user, sha string
		canWrite  bool
	}
	project := func(approvals []forge.Approval) map[approval]int {
		counts := make(map[approval]int)
		for _, a := range approvals {
			counts[approval{a.User, a.SHA, a.CanWrite}]++
		}
		return counts
	}
	if !maps.Equal(project(got.Approvals), project(want.Approvals)) {
		t.Fatalf("PR %d approvals = %+v, want user/SHA/can-write %+v", want.Number, got.Approvals, want.Approvals)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(fmt.Errorf("forge contract: %w", err))
	}
}

func validateObservations(t *testing.T, e Env) {
	t.Helper()
	if len(e.PRs) < 2 {
		t.Fatal("contract needs two PRs")
	}
	fork, sameRepo, writer, reader := false, false, false, false
	for _, pr := range e.Observations {
		fork = fork || !pr.HeadInBaseRepo && len(pr.Approvals) > 0
		sameRepo = sameRepo || pr.HeadInBaseRepo && len(pr.Approvals) > 0
		for _, approval := range pr.Approvals {
			if approval.User == "" || approval.SHA != pr.HeadSHA {
				t.Fatal("fixture approvals must identify a reviewer and the approved head")
			}
			writer = writer || approval.CanWrite
			reader = reader || !approval.CanWrite
		}
	}
	if !fork || !sameRepo || !writer || !reader {
		t.Fatal("contract needs approved fork and same-repo PRs, with writable and non-writable reviewers")
	}
}

// RunDriver checks API contracts without requiring a Git implementation. NewBase
// creates a disposable ref; RefExists observes it independently of Driver.
func RunDriver(t *testing.T, factory func(t *testing.T) Env) {
	t.Helper()
	ctx := context.Background()
	newEnv := func(t *testing.T) Env {
		t.Helper()
		e := factory(t)
		if e.RefExists == nil {
			e.RefExists = func(ref string) bool { _, err := e.Git.ReadRef(ctx, e.Repo, ref); return err == nil }
		}
		validateObservations(t, e)
		return e
	}
	runDriverValidation(t, newEnv)
	t.Run("reconciliation", func(t *testing.T) {
		e := newEnv(t)
		all, err := e.Driver.ListOpenPullRequests(ctx, e.Repo, "")
		must(t, err)
		byNumber := map[int]forge.PullRequestSummary{}
		for _, pr := range all {
			if pr.UpdatedAt.IsZero() {
				t.Fatal("summary lacks updated-at")
			}
			byNumber[pr.Number] = pr
		}
		for _, want := range e.Observations {
			if !want.Closed && byNumber[want.Number].HeadSHA != want.HeadSHA {
				t.Fatalf("missing head for PR %d: %+v", want.Number, all)
			}
		}
		filtered, err := e.Driver.ListOpenPullRequests(ctx, e.Repo, e.Observations[0].Base)
		must(t, err)
		for _, summary := range filtered {
			pr, err := e.Driver.PullRequest(ctx, e.Repo, summary.Number)
			must(t, err)
			if pr.Base != e.Observations[0].Base || pr.Closed {
				t.Fatalf("wrong base: %+v", pr)
			}
		}
		missing, err := e.Driver.ListOpenPullRequests(ctx, e.Repo, "openmerge-contract-missing-base")
		must(t, err)
		if len(missing) != 0 {
			t.Fatalf("base filter ignored: %+v", missing)
		}
	})
	t.Run("fresh-authorization", func(t *testing.T) {
		e := newEnv(t)
		for _, pr := range e.Observations {
			for _, approval := range pr.Approvals {
				if !approval.CanWrite {
					continue
				}
				allowed, err := e.Driver.CanWrite(ctx, e.Repo, approval.User)
				must(t, err)
				if !allowed {
					t.Fatal("fixture writer denied")
				}
				e.SetCanWrite(approval.User, false)
				allowed, err = e.Driver.CanWrite(ctx, e.Repo, approval.User)
				must(t, err)
				if allowed {
					t.Fatal("revoked writer retained command authority")
				}
				current, err := e.Driver.PullRequest(ctx, e.Repo, pr.Number)
				must(t, err)
				for _, a := range current.Approvals {
					if a.User == approval.User && a.CanWrite {
						t.Fatal("revoked writer retained approval authority")
					}
				}
				return
			}
		}
		t.Fatal("fixture lacks writer")
	})
	t.Run("poll-comments", func(t *testing.T) {
		e := newEnv(t)
		pr := e.PRs[0].Number
		e.AddForeignComment(pr, "@openmerge enqueue")
		comments, err := e.Driver.ListComments(ctx, e.Repo, pr, time.Time{})
		must(t, err)
		var command forge.Comment
		for _, c := range comments {
			if c.Body == "@openmerge enqueue" {
				command = c
			}
		}
		if command.ID <= 0 || command.Author == "" || command.CreatedAt.IsZero() {
			t.Fatalf("incomplete command: %+v", command)
		}
		newer, err := e.Driver.ListComments(ctx, e.Repo, pr, command.CreatedAt)
		must(t, err)
		found := false
		for _, c := range newer {
			if c.CreatedAt.Before(command.CreatedAt) {
				t.Fatalf("since filter ignored: %+v", c)
			}
			found = found || c.ID == command.ID
		}
		if !found {
			t.Fatal("inclusive cursor omitted its boundary comment")
		}
		after, err := e.Driver.ListComments(ctx, e.Repo, pr, command.CreatedAt.Add(time.Nanosecond))
		must(t, err)
		for _, c := range after {
			if c.ID == command.ID {
				t.Fatal("cursor included an older comment")
			}
		}
	})
	t.Run("delete-ref", func(t *testing.T) {
		e := newEnv(t)
		ref := e.NewBase(t)
		for range 2 {
			must(t, e.Driver.DeleteRef(ctx, e.Repo, ref))
		}
		if e.RefExists(ref) {
			t.Fatal("deleted ref still exists")
		}
		must(t, e.Driver.DeleteRef(ctx, e.Repo, ref+"-missing"))
	})
	t.Run("comment-retry", func(t *testing.T) {
		e := newEnv(t)
		pr := e.PRs[0].Number
		e.AddForeignComment(pr, "<!-- openmerge:action-1 -->\n<!-- openmerge:key=action-1 -->")
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
		first := forge.Status{State: forge.Queued}
		last := forge.Status{State: forge.Blocked, Reason: "untrusted", Summary: "Approval required"}
		must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, head, first))
		for range 2 {
			must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, head, last))
		}
		assertStatuses(t, e.Statuses(pr), map[string]forge.Status{head: last})
		nextHead := e.PushPR(pr)
		if nextHead == "" || nextHead == head {
			t.Fatal("PushPR did not create a new head")
		}
		must(t, e.Driver.ReportStatus(ctx, e.Repo, pr, nextHead, first))
		assertStatuses(t, e.Statuses(pr), map[string]forge.Status{head: last, nextHead: first})
		otherHead := e.PRs[1].HeadSHA
		must(t, e.Driver.ReportStatus(ctx, e.Repo, e.PRs[1].Number, otherHead, first))
		assertStatuses(t, e.Statuses(pr), map[string]forge.Status{head: last, nextHead: first})
		assertStatuses(t, e.Statuses(e.PRs[1].Number), map[string]forge.Status{otherHead: first})
	})
	t.Run("reads", func(t *testing.T) {
		e := newEnv(t)
		for _, want := range e.Observations {
			pr, err := e.Driver.PullRequest(ctx, e.Repo, want.Number)
			must(t, err)
			assertPullRequest(t, pr, want)
		}
		ci, lint := e.Base+"-ci", e.Base+"-lint"
		e.AddCheckRun(e.PRs[0].HeadSHA, ci, forge.Failure)
		e.AddCheckRun(e.PRs[0].HeadSHA, ci, forge.Pending)
		e.AddCheckRun(e.PRs[0].HeadSHA, lint, forge.Success)
		got, err := e.Driver.CheckRuns(ctx, e.Repo, e.PRs[0].HeadSHA)
		must(t, err)
		for _, run := range got {
			if run.Source == "" {
				t.Fatalf("check lacks a stable source: %+v", run)
			}
		}
		var ciRuns, lintRuns []forge.CheckRun
		for _, run := range got {
			switch run.Name {
			case ci:
				ciRuns = append(ciRuns, run)
			case lint:
				lintRuns = append(lintRuns, run)
			}
		}
		slices.SortFunc(ciRuns, func(a, b forge.CheckRun) int {
			return cmp.Compare(a.ID, b.ID)
		})
		if len(ciRuns) != 2 || ciRuns[1].ID <= ciRuns[0].ID || ciRuns[0].State != forge.Failure || ciRuns[1].State != forge.Pending || len(lintRuns) != 1 || lintRuns[0].State != forge.Success {
			t.Fatalf("CheckRuns lost rerun order or normalized states: %v", got)
		}
	})
	t.Run("approvals-after-push", func(t *testing.T) {
		e := newEnv(t)
		for _, want := range e.Observations {
			if want.Closed || len(want.Approvals) == 0 {
				continue
			}
			before, err := e.Driver.PullRequest(ctx, e.Repo, want.Number)
			must(t, err)
			assertPullRequest(t, before, want)
			nextHead := e.PushPR(want.Number)
			if nextHead == "" || nextHead == want.HeadSHA {
				t.Fatal("PushPR did not create a new head")
			}
			want.HeadSHA = nextHead
			if e.ApprovalsResetOnPush {
				want.Approvals = nil
			}
			after, err := e.Driver.PullRequest(ctx, e.Repo, want.Number)
			must(t, err)
			// Retained approvals keep the old SHA; stale write access is not trusted.
			for i := range want.Approvals {
				want.Approvals[i].CanWrite = false
			}
			assertPullRequest(t, after, want)
		}
	})
	t.Run("mark-landed", func(t *testing.T) {
		e := newEnv(t)
		for _, pr := range e.PRs {
			for range 2 {
				must(t, e.Driver.MarkLanded(ctx, e.Repo, pr.Number, e.BaseSHA))
			}
			got, err := e.Driver.PullRequest(ctx, e.Repo, pr.Number)
			must(t, err)
			if !got.Closed || !e.Landed(pr.Number, e.BaseSHA) {
				t.Fatal("MarkLanded did not converge to a closed PR and one landed commit link")
			}
		}
	})
}
