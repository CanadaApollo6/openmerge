package gitops

import (
	"context"
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestFastForwardPreservesHeadsAndAtomicLeases(t *testing.T) {
	for _, scenario := range []string{"singleton", "stack", "siblings", "reverse", "equal", "stale-head", "moved-base", "moved-fence"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			first := f.heads[1]
			second := f.commit(first, "Author's message\n\nSigned-off-by: Original <original@example.test>", map[string]string{"two": "two"})
			gitCommand(t, f.source, "", nil, "push", "--force", f.remote, second+":"+headRef(2))
			prs := []forge.BuildPR{{Number: 1, HeadSHA: first}, {Number: 2, HeadSHA: second}}
			if scenario == "singleton" {
				prs = prs[:1]
			}
			switch scenario {
			case "siblings":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, f.heads[2]+":"+headRef(2))
				prs[1].HeadSHA = f.heads[2]
			case "reverse":
				prs[0], prs[1] = prs[1], prs[0]
			case "equal":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, first+":"+headRef(2))
				prs[1].HeadSHA = first
			case "stale-head":
				gitCommand(t, f.source, "", nil, "push", "--force", f.remote, f.heads[2]+":"+headRef(2))
			}
			f.g.cfg.PullRequest = func(context.Context, forge.RepoRef, int) (forge.PullRequest, error) {
				t.Fatal("fast-forward read attribution metadata")
				return forge.PullRequest{}, nil
			}
			before := gitCommand(t, f.source, "", nil, "cat-file", "commit", prs[len(prs)-1].HeadSHA)
			ref := "openmerge/queue/main/ff"
			tested, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: f.base, PRs: prs, Method: forge.FastForward, BatchID: "ff"})
			if scenario == "siblings" || scenario == "reverse" || scenario == "equal" || scenario == "stale-head" {
				if err == nil {
					t.Fatal("invalid fast-forward accepted")
				}
				if scenario != "stale-head" && !errors.Is(err, forge.ErrNotFastForward) {
					t.Fatal(err)
				}
				if _, err := f.g.ReadRef(t.Context(), testRepo, ref); err == nil {
					t.Fatal("failed build published a test ref")
				}
				return
			}
			must(t, err)
			if tested == prs[len(prs)-1].HeadSHA || f.read(ref) != tested {
				t.Fatal("fast-forward did not create a marker")
			}
			must(t, f.g.VerifyFastForward(t.Context(), testRepo, f.base, tested, prs[len(prs)-1].HeadSHA))
			f.fetch(tested)
			if got := gitCommand(t, f.source, "", nil, "cat-file", "commit", prs[len(prs)-1].HeadSHA); got != before {
				t.Fatal("commit bytes changed")
			}
			fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
			must(t, err)
			switch scenario {
			case "moved-base":
				f.push("main", f.heads[3], false)
			case "moved-fence":
				_, err = f.g.AdvanceFence(t.Context(), testRepo, testFence, fence, "abandon/other")
				must(t, err)
			}
			baseBefore, fenceBefore := f.read("main"), f.read(testFence)
			_, err = f.g.Land(t.Context(), testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/ff", PRs: prs, FastForwardHeadSHA: prs[len(prs)-1].HeadSHA})
			if scenario == "moved-base" || scenario == "moved-fence" {
				if !errors.Is(err, forge.ErrLeaseRejected) {
					t.Fatal(err)
				}
				if f.read("main") != baseBefore || f.read(testFence) != fenceBefore {
					t.Fatal("rejection moved refs")
				}
				if scenario == "moved-base" {
					_, err = f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: ref, BaseSHA: baseBefore, PRs: prs, Method: forge.FastForward, BatchID: "retry"})
					if !errors.Is(err, forge.ErrNotFastForward) {
						t.Fatalf("diverged head was rebased: %v", err)
					}
				}
				return
			}
			must(t, err)
			if f.read("main") != prs[len(prs)-1].HeadSHA || f.read(testFence) == fence {
				t.Fatal("landing failed to atomically update both refs")
			}
		})
	}
}

func TestHasMergeCommitsInspectsOnlyNewHistory(t *testing.T) {
	f := newFixture(t)
	tree := gitCommand(t, f.source, "", nil, "rev-parse", f.heads[1]+"^{tree}")
	merged := gitCommand(t, f.source, "merge side branch\n", nil, "commit-tree", tree, "-p", f.heads[1], "-p", f.heads[2])
	linearAfterMerge := f.commit(merged, "linear child", map[string]string{"after": "value"})
	// Include a merge through the second parent, beyond the first-parent path.
	sideMerge := gitCommand(t, f.source, "side merge\n", nil, "commit-tree", tree, "-p", f.heads[3], "-p", merged)
	f.push("history", linearAfterMerge, false)
	f.push("side-history", sideMerge, false)
	for _, tc := range []struct {
		name, base, head string
		want             bool
	}{
		{"linear", f.base, f.heads[1], false},
		{"merge at head", f.base, merged, true},
		{"merge inside range", f.base, linearAfterMerge, true},
		{"second parent", f.heads[3], sideMerge, true},
		{"old merge on base", merged, linearAfterMerge, false},
		{"equal", merged, merged, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.g.HasMergeCommits(t.Context(), testRepo, tc.base, tc.head)
			must(t, err)
			if got != tc.want {
				t.Fatalf("has merges=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestFastForwardMarkerIdentityIsUniqueAndAuthoritative(t *testing.T) {
	f := newFixture(t)
	head := f.heads[1]
	req := forge.BuildRequest{Ref: "marker-test", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.FastForward, BatchID: "marker-batch"}
	first, err := f.g.BuildTestRef(t.Context(), testRepo, req)
	must(t, err)
	second, err := f.g.BuildTestRef(t.Context(), testRepo, req)
	must(t, err)
	if first == second {
		t.Fatal("same-clock retry reused a marker SHA")
	}
	for _, marker := range []string{first, second} {
		f.fetch(marker)
		if parents := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%P", marker); parents != head {
			t.Fatalf("marker parents=%s, want only %s", parents, head)
		}
		if tree := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%T", marker); tree != gitCommand(t, f.source, "", nil, "show", "-s", "--format=%T", head) {
			t.Fatal("marker changed the head tree")
		}
		if committer := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%cn <%ce>", marker); committer != f.g.cfg.Committer.Name+" <"+f.g.cfg.Committer.Email+">" {
			t.Fatalf("marker committer=%s", committer)
		}
		message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", marker)
		if forge.ParseBatchMessage(message) != req.BatchID {
			t.Fatalf("marker has no authoritative batch identity: %q", message)
		}
	}
}

func TestFastForwardRejectsInvalidMarkerBeforeStartAndPush(t *testing.T) {
	for _, scenario := range []string{"different tree", "wrong parent", "two parents", "head not descendant", "head equals base"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			head, base := f.heads[1], f.base
			tree := gitCommand(t, f.source, "", nil, "rev-parse", head+"^{tree}")
			parents := []string{"-p", head}
			switch scenario {
			case "different tree":
				tree = gitCommand(t, f.source, "", nil, "rev-parse", f.heads[2]+"^{tree}")
			case "wrong parent":
				parents = []string{"-p", f.base}
			case "two parents":
				parents = append(parents, "-p", f.heads[2])
			case "head not descendant":
				base = f.heads[2]
			case "head equals base":
				base = head
			}
			marker := gitCommand(t, f.source, "OpenMerge test\n\nOpenMerge-Batch: invalid\n", nil, append([]string{"commit-tree", tree}, parents...)...)
			f.push("invalid-marker", marker, false)
			fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
			must(t, err)
			x := executor.Executor{Driver: &r2Driver{}, Git: f.g, Repo: testRepo, Base: "main", AllowNoRequiredChecks: true}
			events := 0
			err = x.Execute(t.Context(), queue.Land{BatchID: "invalid", ExpectedBaseSHA: base, TestedSHA: marker, FenceToken: fence, Method: queue.FastForward, PRs: []queue.BatchPR{{PR: 1, HeadSHA: head}}}, func(queue.Event) error { events++; return nil })
			if err == nil || events != 0 {
				t.Fatalf("invalid marker began landing: events=%d, err=%v", events, err)
			}
			_, err = f.g.Land(t.Context(), testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: marker, Fence: testFence, ExpectedFence: fence, NextFenceID: "land/invalid", PRs: f.prs[:1], FastForwardHeadSHA: head})
			if err == nil || f.read("main") != f.base || f.read(testFence) != fence {
				t.Fatal("invalid marker moved landing refs")
			}
		})
	}
}

func TestFastForwardRecoversLostPushResponseFromFence(t *testing.T) {
	f := newFixture(t)
	fence, err := f.g.EnsureFence(t.Context(), testRepo, testFence)
	must(t, err)
	head := f.heads[1]
	state := queue.State{BaseBranch: "main", BaseSHA: f.base, Fence: fence}
	emit := func(e queue.Event) error {
		_, data, err := queue.EncodeEvent(e)
		if err != nil {
			return err
		}
		restored, err := queue.DecodeEvent(data)
		if err != nil {
			return err
		}
		state = queue.Apply(state, restored)
		return nil
	}
	must(t, emit(queue.PRObserved{PR: 1, HeadSHA: head, Eligible: true, Trusted: true}))
	must(t, emit(queue.EnqueueRequested{PR: 1, At: epoch}))
	cfg := queue.Config{MergeMethod: queue.FastForward}
	x := executor.Executor{Driver: &r2Driver{}, Git: f.g, Repo: testRepo, Base: "main", AllowNoRequiredChecks: true, Now: func() time.Time { return epoch }, NewID: func() (string, error) { return "lost-marker", nil }}
	build := queue.Plan(cfg, state, epoch)[0].(queue.BuildTestRef)
	must(t, x.Execute(t.Context(), build, emit))
	marker := state.Active.TestedSHA
	must(t, emit(queue.ChecksCompleted{TestedSHA: marker, Success: true}))
	land := queue.Plan(cfg, state, epoch)[0].(queue.Land)
	lost := errors.New("push succeeded; response lost")
	x.Git = &r2LostLandResponse{Git: f.g, lost: lost}
	if err := x.Execute(t.Context(), land, emit); !errors.Is(err, lost) {
		t.Fatalf("lost response: %v", err)
	}
	if f.read("main") != head || state.Active.Phase != queue.Landing {
		t.Fatal("push did not precede result persistence")
	}
	_, id, tested, err := f.g.ReadFence(t.Context(), testRepo, testFence)
	must(t, err)
	if id != "land/lost-marker" || tested != marker {
		t.Fatalf("fence lost marker identity: %s tested=%s", id, tested)
	}
	// A restart with different configuration must recover this attempt's head.
	cfg.MergeMethod = queue.Squash
	var resolve queue.Action
	for _, a := range queue.Plan(cfg, state, epoch.Add(time.Hour)) {
		if _, ok := a.(queue.FastForwardResolveLanding); ok {
			resolve = a
		}
	}
	if resolve == nil {
		t.Fatal("missing marker landing resolution")
	}
	_, payload, err := queue.EncodeAction(resolve)
	must(t, err)
	resolve, err = queue.DecodeAction(payload)
	must(t, err)
	x.Git = f.g
	f.push("main", f.base, true)
	var absent *executor.ErrLandingNotOnBase
	if err := x.Execute(t.Context(), resolve, emit); !errors.As(err, &absent) || absent.TestedSHA != head {
		t.Fatalf("rollback was not checked against landing head: %v", err)
	}
	f.push("main", head, false)
	must(t, x.Execute(t.Context(), resolve, emit))
	if state.Active != nil || state.BaseSHA != head {
		t.Fatal("recovery recorded the marker instead of landed head")
	}
	for _, action := range queue.Plan(cfg, state, epoch.Add(time.Hour)) {
		if cleanup, ok := action.(queue.DeleteHeadBranch); ok && cleanup.LandedSHA != head {
			t.Fatal("cleanup refers to marker")
		}
		if _, ok := action.(queue.MarkLanded); ok {
			t.Fatal("fast-forward should use native merged state")
		}
	}
}
