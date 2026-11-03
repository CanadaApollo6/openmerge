package gitops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func nextOperation(t *testing.T, samples <-chan OperationStats, name string) OperationStats {
	t.Helper()
	select {
	case s := <-samples:
		if s.Operation != name || s.Repository != testRepo.Path || s.Wait <= 0 || s.Hold <= 0 {
			t.Fatalf("unexpected operation stats: %+v, want %s", s, name)
		}
		return s
	case <-time.After(5 * time.Second):
		t.Fatalf("no sample for %s", name)
		return OperationStats{}
	}
}

func TestOperationObserverContention(t *testing.T) {
	f := newFixture(t)
	samples := make(chan OperationStats, 4)
	f.g.cfg.Observe = func(s OperationStats) { samples <- s }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	remote := f.g.cfg.Remote
	f.g.cfg.Remote = func(ctx context.Context, repo forge.RepoRef) (string, Auth, error) {
		once.Do(func() { close(entered); <-release })
		return remote(ctx, repo)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := f.g.ReadRef(t.Context(), testRepo, "main"); first <- err }()
	<-entered
	go func() { _, err := f.g.ReadRef(t.Context(), testRepo, "main"); second <- err }()
	select {
	case <-second:
		t.Fatal("second operation bypassed the cache lock")
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	must(t, <-first)
	must(t, <-second)
	a, b := nextOperation(t, samples, "read-ref"), nextOperation(t, samples, "read-ref")
	if max(a.Wait, b.Wait) < 50*time.Millisecond || max(a.Hold, b.Hold) < 50*time.Millisecond || a.Maintenance <= 0 || b.Maintenance <= 0 {
		t.Fatalf("missing contention or maintenance timings: %+v %+v", a, b)
	}
	select {
	case s := <-samples:
		t.Fatalf("duplicate sample: %+v", s)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestOperationObserverNamesAndErrors(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
	f.build(forge.Merge, f.prs[:1], "observed")
	samples := make(chan OperationStats, 32)
	f.g.cfg.Observe = func(s OperationStats) { samples <- s }
	ctx := t.Context()
	opts := forge.PushOptions{}
	tested := f.base
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"build-test-ref", func() { f.build(forge.Merge, f.prs[:1], "observed") }},
		{"land", func() {
			_, _ = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: f.base, Fence: testFence, ExpectedFence: f.base, NextFenceID: "observed", PRs: f.prs[:1]}, opts)
		}},
		{"read-ref", func() { _, _ = f.g.ReadRef(ctx, testRepo, "missing") }},
		{"has-merge-commits", func() { _, _ = f.g.HasMergeCommits(ctx, testRepo, f.base, f.heads[1]) }},
		{"is-ancestor", func() { _, _ = f.g.IsAncestor(ctx, testRepo, f.base, f.heads[1]) }},
		{"revision-diff", func() {
			_, _ = f.g.RevisionDiff(ctx, testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: f.heads[1]})
		}},
		{"governing-files", func() { _, _ = f.g.GoverningFiles(ctx, testRepo, forge.GoverningFilesRequest{CommitSHA: f.base}) }},
		{"commits-between", func() { _, _, _ = f.g.CommitsBetween(ctx, testRepo, f.base, f.heads[1], 10) }},
		{"read-prepared-ref", func() { _, _ = f.g.ReadPreparedRef(ctx, testRepo, "missing") }},
		{"advance-fence", func() { _, _ = f.g.AdvanceFence(ctx, testRepo, testFence, f.base, "next", opts) }},
		{"ensure-fence", func() { _, _ = f.g.EnsureFence(ctx, testRepo, testFence, opts) }},
		{"read-fence", func() { _, _, _, _ = f.g.ReadFence(ctx, testRepo, testFence) }},
		{"verify-fast-forward", func() { _ = f.g.VerifyFastForward(ctx, testRepo, f.base, tested, f.heads[1]) }},
		{"delete-branch", func() { _ = f.g.DeleteBranch(ctx, testRepo, "missing", f.base, opts) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.call()
			s := nextOperation(t, samples, tc.name)
			if s.Maintenance <= 0 {
				t.Fatalf("missing maintenance: %+v", s)
			}
		})
	}
	lease, err := f.g.AcquireCommits(ctx, testRepo, []string{f.base})
	must(t, err)
	nextOperation(t, samples, "acquire-commits")
	for range 2 {
		lease()
		if s := nextOperation(t, samples, "release-commits"); s.Maintenance != 0 {
			t.Fatalf("release ran maintenance: %+v", s)
		}
	}
	for _, tc := range []struct {
		name string
		call func(string)
	}{
		{"read-commit-message", func(sha string) { _, _ = f.g.ReadCommitMessage(ctx, testRepo, sha) }},
		{"first-parent-commits", func(sha string) { _, _, _ = f.g.FirstParentCommits(ctx, testRepo, f.base, sha, 10) }},
		{"local-config", func(sha string) { _, _ = f.g.LocalConfig(ctx, testRepo, sha) }},
		{"local-governing-files", func(sha string) { _, _ = f.g.LocalGoverningFiles(ctx, testRepo, sha) }},
	} {
		for _, sha := range []string{f.heads[1], strings.Repeat("9", 40)} {
			tc.call(sha)
			if s := nextOperation(t, samples, tc.name); s.Maintenance != 0 {
				t.Fatalf("local reader ran maintenance: %+v", s)
			}
		}
	}
	// A resolver error occurs after acquisition but before maintenance is allowed.
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		return "", Auth{}, errors.New("unavailable")
	}
	if _, err := f.g.ReadRef(ctx, testRepo, "main"); err == nil {
		t.Fatal("resolver error lost")
	}
	if s := nextOperation(t, samples, "read-ref"); s.Maintenance != 0 {
		t.Fatalf("failed open ran maintenance: %+v", s)
	}
	select {
	case s := <-samples:
		t.Fatalf("duplicate sample: %+v", s)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestOperationObserverDoesNotBlockOrPanic(t *testing.T) {
	f := newFixture(t)
	f.build(forge.Merge, f.prs[:1], "observer")
	s, unlock, err := f.g.open(t.Context(), testRepo, "test")
	must(t, err)
	unlock()
	lock, _ := cacheLocks.Load(s.dir)
	mu := lock.(*sync.Mutex)
	outsideLock := make(chan bool, 2)
	entered, release, finished := make(chan struct{}, 4), make(chan struct{}), make(chan struct{}, 4)
	f.g.cfg.Observe = func(OperationStats) {
		free := mu.TryLock()
		if free {
			mu.Unlock()
		}
		outsideLock <- free
		entered <- struct{}{}
		defer func() { finished <- struct{}{} }()
		<-release
		panic("observer failure")
	}
	t.Cleanup(func() {
		close(release)
		for range 2 {
			select {
			case <-finished:
			case <-time.After(time.Second):
			}
		}
	})
	for range 2 {
		done := make(chan error, 1)
		go func() { _, err := f.g.ReadCommitMessage(t.Context(), testRepo, f.base); done <- err }()
		select {
		case err := <-done:
			must(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("observer blocked an operation or held its lock")
		}
		<-entered
		if !<-outsideLock {
			t.Fatal("observer ran while the cache lock was held")
		}
	}
}

func TestOperationFailedAcquisitionHasNoSample(t *testing.T) {
	f := newFixture(t)
	_, unlock, err := f.g.open(t.Context(), testRepo, "test")
	must(t, err)
	defer unlock()
	samples := make(chan OperationStats, 4)
	f.g.cfg.Observe = func(s OperationStats) { samples <- s }
	if _, err := f.g.LocalConfig(t.Context(), testRepo, f.base); err == nil {
		t.Fatal("TryLock succeeded on busy cache")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.g.ReadRef(ctx, testRepo, "main"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context polling lost deadline: %v", err)
	}
	select {
	case s := <-samples:
		t.Fatalf("reported an unacquired lock: %+v", s)
	case <-time.After(30 * time.Millisecond):
	}
}
