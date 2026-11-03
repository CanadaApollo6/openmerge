package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

// TestTreeDedupeShadowRealGit builds real queue test refs with the production
// builder and keys them with Git's own readers. It covers the same tree after
// a rebuild, a bisection right half equal to the failed batch, a changed
// workflow blob, an unknown runner image, and unconfirmed versus confirmed
// failures.
func TestTreeDedupeShadowRealGit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	noWorkflowBase := f.base
	f.base = f.commit(f.base, "workflows", map[string]string{".github/workflows/ci.yml": "on: push\n"})
	f.push("main", f.base, false)
	workflowBase := f.base
	pair := []forge.BuildPR{f.prs[0], f.prs[1]}
	key := func(sha, image string) (string, []treededupe.UnknownInput) {
		t.Helper()
		in, err := f.g.TreeDedupeInputs(ctx, testRepo, sha)
		return treededupe.Key(treededupe.Inputs{Namespace: "7/" + testRepo.Path + "/main/", Git: in, GitErr: err, Checks: "and (check-success = ci)", RunnerImage: image})
	}
	const image = "ubuntu24/20261020.1"
	at := func(minute int) time.Time { return epoch.Add(time.Duration(minute) * time.Minute) }

	batch := f.build(forge.Merge, pair, "b1")
	in, err := f.g.TreeDedupeInputs(ctx, testRepo, batch)
	must(t, err)
	if len(in.Workflows) != 1 || in.Workflows[0].Path != ".github/workflows/ci.yml" || in.Workflows[0].Type != "blob" || in.Workflows[0].Mode != "100644" {
		t.Fatalf("workflow listing = %+v", in.Workflows)
	}
	if want := gitCommand(t, filepath.Join(f.g.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(testRepo.Path)))), "", nil, "rev-parse", batch+"^{tree}"); in.Tree != want {
		t.Fatalf("tree = %s, git rev-parse = %s", in.Tree, want)
	}
	batchKey, unknown := key(batch, image)
	if batchKey == "" || unknown != nil {
		t.Fatalf("batch key unknown: %v", unknown)
	}

	t.Run("rebuild after invalidation reuses a success", func(t *testing.T) {
		rebuilt := f.client(t, 60)
		f.g, rebuilt = rebuilt, f.g
		defer func() { f.g = rebuilt }()
		again := f.build(forge.Merge, pair, "b1-rebuild")
		if again == batch {
			t.Fatal("rebuild produced the same commit; the test needs distinct SHAs")
		}
		againKey, _ := key(again, image)
		if againKey != batchKey {
			t.Fatal("rebuilt tree has a different key")
		}
		var l treededupe.Ledger
		l.ObserveBuild(treededupe.Build{At: at(0), BatchID: "b1", SHA: batch, Kind: treededupe.KindBatch, Key: batchKey})
		l.ObserveOutcome(batch, at(10), true)
		l.ObserveBuild(treededupe.Build{At: at(11), BatchID: "b1-rebuild", SHA: again, Kind: treededupe.KindBatch, Key: againKey})
		if s := l.Summarize(time.Time{}, time.Time{}); s.WouldReuseSuccess != 1 || s.AvoidableMinutes != 10 {
			t.Fatalf("summary = %+v", s)
		}
	})

	t.Run("bisection right half equals the failed batch", func(t *testing.T) {
		left := f.build(forge.Merge, []forge.BuildPR{f.prs[0]}, "b2-left")
		f.base = left // the left half landed
		defer func() { f.base = workflowBase }()
		right := f.build(forge.Merge, []forge.BuildPR{f.prs[1]}, "b3-right")
		rightKey, _ := key(right, image)
		if rightKey != batchKey {
			t.Fatal("right half on the landed left half does not reproduce the batch tree")
		}
		for _, confirmed := range []bool{false, true} {
			var l treededupe.Ledger
			l.ObserveBuild(treededupe.Build{At: at(0), BatchID: "b1", SHA: batch, Kind: treededupe.KindBatch, Key: batchKey, RetryConfigured: true})
			l.ObserveOutcome(batch, at(10), false)
			if confirmed {
				l.ObserveConfirmedFailure("b1", batch, at(20))
			}
			l.ObserveBuild(treededupe.Build{At: at(30), BatchID: "b3-right", SHA: right, Kind: treededupe.KindBisection, Key: rightKey, Singleton: true, RetryConfigured: true})
			s := l.Summarize(time.Time{}, time.Time{})
			if want := map[bool]int{false: 0, true: 1}[confirmed]; s.WouldReuseFailure != want || s.WouldReuseSuccess != 0 {
				t.Fatalf("confirmed=%v summary = %+v", confirmed, s)
			}
		}
	})

	t.Run("different workflow blob misses", func(t *testing.T) {
		f.base = f.commit(workflowBase, "workflow edit", map[string]string{".github/workflows/ci.yml": "on: [push]\n"})
		f.push("main", f.base, false)
		defer func() { f.base = workflowBase }()
		changed := f.build(forge.Merge, pair, "b4")
		in, err := f.g.TreeDedupeInputs(ctx, testRepo, changed)
		must(t, err)
		if in.Workflows[0].OID == mustInputs(t, f, batch).Workflows[0].OID {
			t.Fatal("workflow blob did not change")
		}
		if changedKey, _ := key(changed, image); changedKey == batchKey || changedKey == "" {
			t.Fatal("changed workflow blob kept the key")
		}
	})

	t.Run("unknown runner image never hits", func(t *testing.T) {
		got, unknown := key(batch, "")
		if got != "" || len(unknown) != 1 || unknown[0] != treededupe.UnknownRunnerImage {
			t.Fatalf("key=%q unknown=%v", got, unknown)
		}
		var l treededupe.Ledger
		l.ObserveBuild(treededupe.Build{At: at(0), BatchID: "b1", SHA: batch, Kind: treededupe.KindBatch, Unknown: treededupe.Unknowns(unknown)})
		l.ObserveOutcome(batch, at(10), true)
		l.ObserveBuild(treededupe.Build{At: at(11), BatchID: "b5", SHA: strings.Repeat("e", 40), Kind: treededupe.KindBatch, Unknown: treededupe.Unknowns(unknown)})
		if s := l.Summarize(time.Time{}, time.Time{}); s.WouldReuseSuccess != 0 || s.KeyUnknown["runner_image"] != 2 {
			t.Fatalf("summary = %+v", s)
		}
	})

	t.Run("missing local commit and absent workflows are unknown", func(t *testing.T) {
		if _, unknown := key(strings.Repeat("9", 40), image); len(unknown) == 0 {
			t.Fatal("missing commit produced a key")
		}
		f.base = noWorkflowBase
		defer func() { f.base = workflowBase }()
		bare := f.build(forge.Merge, pair, "b6")
		if got, unknown := key(bare, image); got != "" || len(unknown) != 1 || unknown[0] != treededupe.UnknownWorkflows {
			t.Fatalf("no-workflow key=%q unknown=%v", got, unknown)
		}
	})
}

func mustInputs(t *testing.T, f *fixture, sha string) treededupe.GitInputs {
	t.Helper()
	in, err := f.g.TreeDedupeInputs(context.Background(), testRepo, sha)
	must(t, err)
	return in
}

// TestTreeDedupeShadowNeverBlocksLanding pins Astra round 1: the shadow read
// holds no lock landing needs. A wrapper Git parks the shadow inside its
// ls-tree; a landing session must open the same cache while the shadow is
// provably still in flight, and the shadow must then finish while landing
// still holds the cache mutex.
func TestTreeDedupeShadowNeverBlocksLanding(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	batch := f.build(forge.Merge, []forge.BuildPR{f.prs[0]}, "b1")
	gate := t.TempDir()
	started, release := filepath.Join(gate, "started"), filepath.Join(gate, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	wrapper := filepath.Join(gate, "git")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = ls-tree ]; then\n    : > '" + started + "'\n" +
		"    i=0; while [ ! -e '" + release + "' ] && [ $i -lt 1000 ]; do sleep 0.01; i=$((i+1)); done\n    break\n  fi\ndone\n" +
		"exec '" + f.g.git + "' \"$@\"\n"
	must(t, os.WriteFile(wrapper, []byte(script), 0o700))
	shadow := *f.g
	shadow.git = wrapper
	done := make(chan error, 1)
	go func() {
		_, err := shadow.TreeDedupeInputs(ctx, testRepo, batch)
		done <- err
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shadow read never reached ls-tree")
		}
	}

	// Landing arrives while the shadow is parked mid-read and must not wait.
	landingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, unlock, err := f.g.open(landingCtx, testRepo, "test")
	if err != nil {
		t.Fatalf("landing waited on the in-flight shadow read: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("shadow finished before release (%v); the test proved nothing", err)
	default:
	}

	// The shadow needs no lock: it completes while landing holds the cache.
	must(t, os.WriteFile(release, nil, 0o600))
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("shadow read waited on landing's cache lock")
	}
	unlock()
}
