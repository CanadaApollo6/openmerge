package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestReviewDuplicateLandAfterRollback(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "duplicate")
	req := forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: initial, NextFenceID: "land/same-attempt", PRs: f.prs[:1]}
	// Precompute the winner's exact fence object without changing remote refs.
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	next, err := s.fence(ctx, initial, req.NextFenceID, tested)
	must(t, err)
	cache := s.dir
	unlock()
	gitPath, err := exec.LookPath("git")
	must(t, err)
	wrapper := filepath.Join(t.TempDir(), "git")
	// A duplicate has passed Land's preflight and minted its fence. Before its
	// push advertises refs, the original copy lands and someone rolls main back.
	script := "for a in \"$@\"; do\nif [ \"$a\" = push ]; then\n" +
		shellQuote(gitPath) + " --git-dir=" + shellQuote(cache) + " push --atomic --force-with-lease=refs/heads/main:" + f.base + " --force-with-lease=refs/heads/" + testFence + ":" + initial + " -- " + shellQuote(f.remote) + " " + tested + ":refs/heads/main " + next + ":refs/heads/" + testFence + " >/dev/null 2>&1 || exit 90\n" +
		shellQuote(gitPath) + " --git-dir=" + shellQuote(f.remote) + " update-ref refs/heads/main " + f.base + " " + tested + " || exit 91\nbreak\nfi\ndone\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	writeExecutable(t, wrapper, script)
	f.g.git = wrapper
	result, err := f.g.Land(ctx, testRepo, req)
	base := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main")
	fence := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence)
	t.Logf("duplicate returned fence=%s err=%v; main restored to tested=%v; fence unchanged from original landing=%v", result, err, base == tested, fence == next)
	if !errors.Is(err, forge.ErrLeaseRejected) || result != "" || base != f.base || fence != next {
		t.Fatal("delayed duplicate did not refuse the consumed fence lease without changing refs")
	}
}

func TestReviewRebaseMergeResolution(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	left := f.commit(f.base, "left", map[string]string{"left.txt": "left"})
	right := f.commit(f.base, "side", map[string]string{"discarded.txt": "not in PR head"})
	tree := gitCommand(t, f.source, "", nil, "rev-parse", left+"^{tree}")
	// A valid 'git merge -s ours' result: the side history is incorporated but
	// deliberately not its file. Replaying must not silently resurrect it.
	head := gitCommand(t, f.source, "merge side using ours\n", nil, "commit-tree", tree, "-p", left, "-p", right)
	gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(42))
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			f.g.explicitBase = !fallback
			tested, err := f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/review", BaseSHA: f.base, PRs: []forge.BuildPR{{Number: 42, HeadSHA: head}}, Method: forge.Rebase, BatchID: "review"})
			if !errors.Is(err, forge.ErrConflict) || tested != "" || !strings.Contains(err.Error(), "rebase cannot replay merge commits in PR #42; use the merge or squash method") {
				t.Fatalf("merge resolution was not refused as a conflict: sha=%q err=%v", tested, err)
			}
			if _, err := f.g.ReadRef(ctx, testRepo, "openmerge/queue/review"); err == nil {
				t.Fatal("refused replay published a test ref")
			}
			if f.read("main") != f.base {
				t.Fatal("refused replay changed main")
			}
		})
	}
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash} {
		tested := f.build(method, []forge.BuildPR{{Number: 42, HeadSHA: head}}, "merge-resolution")
		if got := gitCommand(t, f.remote, "", nil, "rev-parse", tested+"^{tree}"); got != tree {
			t.Fatalf("%s changed the resolved PR tree", method)
		}
	}
}

func TestReviewHostileRefsAndHooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, ref := range []string{"--upload-pack=touch /tmp/pwned", "../main", "main space", "main:refs/heads/other", "main\nrefs/heads/other"} {
		if _, err := f.g.ReadRef(ctx, testRepo, ref); err == nil {
			t.Errorf("accepted invalid ref %q", ref)
		}
	}
	before := f.g.cfg.HeadRef
	for _, ref := range []string{"--upload-pack=touch /tmp/pwned", "refs/pull/1/../head", "refs/pull/1 head", "refs/pull/1:refs/heads/main"} {
		f.g.cfg.HeadRef = func(int) string { return ref }
		_, err := f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/hostile", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: "hostile"})
		if err == nil {
			t.Errorf("accepted invalid head ref %q", ref)
		}
	}
	f.g.cfg.HeadRef = before
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	cache := s.dir
	unlock()
	marker := filepath.Join(t.TempDir(), "credentials-leaked")
	must(t, os.MkdirAll(filepath.Join(cache, "hooks"), 0700))
	writeExecutable(t, filepath.Join(cache, "hooks", "pre-push"), "printf '%s' \"$OPENMERGE_GIT_PASSWORD\" > "+shellQuote(marker)+"\nexit 1\n")
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return f.remote, Auth{Username: "user", Password: "review-secret"}, nil
	}
	f.build(forge.Merge, f.prs[:1], "hooks")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hook ran: %v", err)
	}
	st, err := os.Stat(f.g.askpass)
	must(t, err)
	if st.Mode().Perm() != 0700 {
		t.Errorf("helper permissions=%o", st.Mode().Perm())
	}
	data, err := os.ReadFile(f.g.askpass)
	must(t, err)
	if strings.Contains(string(data), "review-secret") {
		t.Fatal("secret in helper")
	}
}

func TestReviewSameCacheInstances(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCThresholdBytes = 1
	ctx := context.Background()
	other, err := New(ctx, f.g.cfg)
	must(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.g
			if i%2 == 1 {
				r = other
			}
			_, err := r.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: fmt.Sprintf("openmerge/queue/concurrent-%d", i), BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: fmt.Sprintf("c%d", i)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
}

func TestReviewEmptyAndAlreadyMerged(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		f := newFixture(t)
		empty := f.commit(f.base, "empty", nil)
		gitCommand(t, f.source, "", nil, "push", f.remote, empty+":"+headRef(42))
		f.build(method, []forge.BuildPR{{Number: 42, HeadSHA: empty}}, "empty")
		gitCommand(t, f.source, "", nil, "push", f.remote, f.base+":"+headRef(43))
		sha, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/already", BaseSHA: f.base, PRs: []forge.BuildPR{{Number: 43, HeadSHA: f.base}}, Method: method, BatchID: "already"})
		if method == forge.Rebase {
			if err == nil || sha != "" {
				t.Fatalf("rebase accepted a PR with no commits to replay: sha=%q err=%v", sha, err)
			}
		} else {
			must(t, err)
			if got := gitCommand(t, f.remote, "", nil, "rev-parse", sha+"^{tree}"); got != gitCommand(t, f.remote, "", nil, "rev-parse", f.base+"^{tree}") {
				t.Fatalf("%s changed an already-merged tree", method)
			}
		}
	}
}

func TestReviewBinaryAndRenameConflict(t *testing.T) {
	for _, kind := range []string{"binary", "rename"} {
		f := newFixture(t)
		base := f.commit(f.base, "base with file", map[string]string{"file": "same\n"})
		var left, right string
		if kind == "binary" {
			left = f.commit(base, "left binary", map[string]string{"file": "\x00left"})
			right = f.commit(base, "right binary", map[string]string{"file": "\x00right"})
		} else {
			for i, name := range []string{"left.txt", "right.txt"} {
				index := filepath.Join(t.TempDir(), "index")
				env := []string{"GIT_INDEX_FILE=" + index}
				gitCommand(t, f.source, "", env, "read-tree", base)
				blob := gitCommand(t, f.source, "", nil, "rev-parse", base+":file")
				gitCommand(t, f.source, "0 "+strings.Repeat("0", 40)+"\tfile\n", env, "update-index", "--index-info")
				gitCommand(t, f.source, "", env, "update-index", "--add", "--cacheinfo", "100644", blob, name)
				tree := gitCommand(t, f.source, "", env, "write-tree")
				c := gitCommand(t, f.source, name+"\n", nil, "commit-tree", tree, "-p", base)
				if i == 0 {
					left = c
				} else {
					right = c
				}
			}
		}
		gitCommand(t, f.source, "", nil, "push", f.remote, left+":"+headRef(42), right+":"+headRef(43))
		for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
			_, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/conflicts", BaseSHA: base, PRs: []forge.BuildPR{{Number: 42, HeadSHA: left}, {Number: 43, HeadSHA: right}}, Method: method, BatchID: "conflict"})
			if !errors.Is(err, forge.ErrConflict) {
				t.Errorf("%s %s returned %v", kind, method, err)
			}
		}
	}
}

// This reproduction records the residual external-writer race described in
// README.md. It is indistinguishable from an administrator rolling main back
// after landing; uniqueness prevents duplicates, not arbitrary external rewrites.
func TestReviewExternalTestedThenRollbackLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence)
	must(t, err)
	tested := f.build(forge.Merge, f.prs[:1], "external")
	gitPath, err := exec.LookPath("git")
	must(t, err)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	must(t, os.MkdirAll(filepath.Join(f.remote, "hooks"), 0700))
	writeExecutable(t, filepath.Join(f.remote, "hooks", "pre-receive"), "cat > "+shellQuote(marker)+"\nunset GIT_QUARANTINE_PATH GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES\n"+shellQuote(gitPath)+" --git-dir="+shellQuote(f.remote)+" update-ref refs/heads/main "+f.base+" "+tested+"\n")
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = push ]; then\n"+shellQuote(gitPath)+" --git-dir="+shellQuote(f.remote)+" update-ref refs/heads/main "+tested+" "+f.base+" || exit 90\nbreak\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper
	next, err := f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: initial, NextFenceID: "land/external", PRs: f.prs[:1]})
	must(t, err)
	data, err := os.ReadFile(marker)
	must(t, err)
	base := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main")
	t.Logf("receiver updates=%s; Land returned %s; main rolled back before fence write=%v", data, next, base == f.base)
	fence := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence)
	if base != f.base || fence != next || strings.Contains(string(data), " refs/heads/main") {
		t.Fatal("external-writer reproduction no longer stages the documented fence-only push")
	}
}
