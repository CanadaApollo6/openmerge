package gitops

import (
	"context"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestPushLeasesAgainstRealGit(t *testing.T) {
	for _, moved := range []string{"forward", "rollback", "fence"} {
		t.Run(moved, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
			must(t, err)
			current := f.build(forge.Merge, f.prs[:1], "current")
			f.fetch(current)
			f.push("main", current, false)
			tested, err := f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/tested", BaseSHA: current,
				PRs: f.prs[1:2], Method: forge.Merge, BatchID: "tested"})
			must(t, err)
			switch moved {
			case "forward":
				forward := f.commit(current, "external forward", map[string]string{"external.txt": "forward"})
				f.push("main", forward, false)
			case "rollback":
				f.push("main", f.base, true)
			case "fence":
				_, err := f.g.AdvanceFence(ctx, testRepo, testFence, initial, "abandon/other")
				must(t, err)
			}
			beforeBase, beforeFence := f.read("main"), f.read(testFence)
			s, unlock, err := f.g.open(ctx, testRepo)
			must(t, err)
			defer unlock()
			next, err := s.fence(ctx, initial, "land/lease", tested)
			must(t, err)
			// Bypass Land's preflight: prove receive-pack/CLI leases themselves
			// refuse stale values, including rollbacks and fence-only moves.
			err = s.pushLease(ctx, "--force-with-lease=refs/heads/main:"+current,
				"--force-with-lease=refs/heads/"+testFence+":"+initial, "--", s.remote,
				tested+":refs/heads/main", next+":refs/heads/"+testFence)
			if err != forge.ErrLeaseRejected {
				t.Fatalf("push returned %v, want only ErrLeaseRejected", err)
			}
			if got := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main"); got != beforeBase {
				t.Fatal("atomic refusal moved base")
			}
			if got := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence); got != beforeFence {
				t.Fatal("atomic refusal moved fence")
			}
		})
	}
}

func TestGitSkipsLeaseForUpToDateRef(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "noop")
	f.fetch(tested)
	f.push("main", tested, false)
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	defer unlock()
	next, err := s.fence(ctx, initial, "land/noop", tested)
	must(t, err)
	out, diagnostic, err := s.run(ctx, "", "push", "--atomic", "--porcelain",
		"--force-with-lease=refs/heads/main:"+f.base,
		"--force-with-lease=refs/heads/"+testFence+":"+initial, "--", s.remote,
		tested+":refs/heads/main", next+":refs/heads/"+testFence)
	// This is an upstream Git behavior test: the stale base lease is ignored
	// because base is already tested, yet the fence still advances.
	if err != nil || !strings.Contains(out, "[up to date]") {
		t.Fatalf("git no-op lease behavior changed: %v\n%s\n%s", err, out, diagnostic)
	}
	if got := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence); got != next {
		t.Fatal("test did not reproduce the stale-lease fence-only update")
	}
}

func TestLandRefusesStaleUpToDateBase(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "noop")
	f.fetch(tested)
	f.push("main", tested, false)
	_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
		Fence: testFence, ExpectedFence: initial, NextFenceID: "land/noop", PRs: f.prs[:1]})
	if err != forge.ErrLeaseRejected || f.read("main") != tested || f.read(testFence) != initial {
		t.Fatalf("stale up-to-date base was not refused without effects: %v", err)
	}
}

func TestAtomicUnsupportedIsInfrastructureFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "atomic")
	gitCommand(t, f.remote, "", nil, "config", "receive.advertiseAtomic", "false")
	_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
		Fence: testFence, ExpectedFence: initial, NextFenceID: "land/atomic", PRs: f.prs[:1]})
	if err == nil || err == forge.ErrLeaseRejected || !strings.Contains(err.Error(), "does not support --atomic") {
		t.Fatalf("unsupported atomic transport error = %v", err)
	}
	if f.read("main") != f.base || f.read(testFence) != initial {
		t.Fatal("unsupported atomic push changed a ref")
	}
}

func TestFenceCommitShape(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	next, err := f.g.AdvanceFence(ctx, testRepo, testFence, initial, "abandon/shape")
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "fence")
	landed, err := f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
		Fence: testFence, ExpectedFence: next, NextFenceID: "land/shape", PRs: f.prs[:1]})
	must(t, err)
	for _, tc := range []struct{ sha, parent, message string }{
		{initial, "", "openmerge fence initial"},
		{next, initial, "openmerge fence abandon/shape"},
		{landed, next, "openmerge fence land/shape tested=" + tested},
	} {
		f.fetch(tc.sha)
		if got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%P", tc.sha); got != tc.parent {
			t.Fatalf("fence parent = %q, want %q", got, tc.parent)
		}
		if got := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", tc.sha); got != tc.message {
			t.Fatalf("fence message = %q, want %q", got, tc.message)
		}
		if tree := gitCommand(t, f.source, "", nil, "ls-tree", "-r", tc.sha); tree != "" {
			t.Fatalf("fence tree is not empty: %s", tree)
		}
	}
}
