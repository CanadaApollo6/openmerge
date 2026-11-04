package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func gitCacheFixture(t *testing.T) (string, string) {
	t.Helper()
	remote := t.TempDir()
	var sha string
	for _, args := range [][]string{
		{"init", "--template=", "-b", "main", remote},
		{"-C", remote, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "--allow-empty", "-m", "fixture"},
		{"-C", remote, "rev-parse", "HEAD"},
	} {
		out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		sha = strings.TrimSpace(string(out))
	}
	return remote, sha
}

func gitCacheResponse(t *testing.T, h http.Handler) status.GitCacheSnapshot {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/git-cache", nil)
	r.Header.Set("Authorization", "Bearer fixture-read-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body struct {
		Version int                     `json:"version"`
		Cache   status.GitCacheSnapshot `json:"git_cache"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Version != 1 {
		t.Fatalf("git cache response: %d %s (%v)", w.Code, w.Body, err)
	}
	return body.Cache
}

func awaitGitCache(t *testing.T, h http.Handler, ready func(status.GitCacheSnapshot) bool) status.GitCacheSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := gitCacheResponse(t, h)
		if ready(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("observer did not deliver expected aggregates: %+v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestGitCacheRealContention(t *testing.T) {
	remote, _ := gitCacheFixture(t)
	keys := []store.QueueKey{{Installation: "7", Repository: "owner/repo", BaseBranch: "main"}}
	cache := status.NewGitCache(func() []store.QueueKey { return keys })
	h := status.DynamicHandler(nil, func() []store.QueueKey { return keys }, "fixture-read-token", nil, status.ReadOptions{GitCache: cache})
	entered, release := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	r, err := gitops.New(t.Context(), gitops.Config{
		WorkDir: t.TempDir(), HeadRef: func(int) string { return "refs/heads/main" },
		Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) {
			once.Do(func() { close(entered); <-release })
			return remote, gitops.Auth{}, nil
		},
		GCInterval: -1, GCThresholdBytes: -1, Observe: observeGitCacheOperations(nil, cache),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := r.ReadRef(t.Context(), forge.RepoRef{Path: "owner/repo"}, "main"); first <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first operation never acquired the lock")
	}
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := r.ReadRef(t.Context(), forge.RepoRef{Path: "owner/repo"}, "main")
		second <- err
	}()
	<-started
	select {
	case err := <-second:
		t.Fatalf("second operation bypassed the lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	unblock.Do(func() { close(release) })
	for _, done := range []<-chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Git operation did not complete")
		}
	}
	snapshot := awaitGitCache(t, h, func(s status.GitCacheSnapshot) bool {
		return s.Repositories["owner/repo"].Operations["read-ref"].Count == 2
	})
	op := snapshot.Repositories["owner/repo"].Operations["read-ref"]
	var waits, holds uint64
	for i := 7; i < len(op.Wait.Buckets); i++ {
		waits += op.Wait.Buckets[i]
		holds += op.Hold.Buckets[i]
	}
	if waits+op.Wait.Overflow == 0 || holds+op.Hold.Overflow == 0 || op.Maintenance.Buckets[0] != 0 {
		t.Fatalf("real contention or maintenance missing from API: %+v", op)
	}
	if os.Getenv("OPENMERGE_GIT_CACHE_CURL") == "1" {
		server := httptest.NewServer(h)
		defer server.Close()
		out, err := exec.CommandContext(t.Context(), "curl", "--fail", "--silent", "--show-error", "-H", "Authorization: Bearer fixture-read-token", server.URL+"/v1/git-cache").CombinedOutput()
		if err != nil {
			t.Fatalf("curl: %v %s", err, out)
		}
		t.Logf("curl %s/v1/git-cache\n%s", server.URL, out)
	}
}

func TestGitCacheRealGCFailure(t *testing.T) {
	remote, sha := gitCacheFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = gc ]; then exit 1; fi\ndone\nexec \"$OPENMERGE_TEST_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENMERGE_TEST_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	keys := []store.QueueKey{{Repository: "owner/repo"}}
	cache := status.NewGitCache(func() []store.QueueKey { return keys })
	h := status.Handler(nil, keys, "fixture-read-token", nil, status.ReadOptions{GitCache: cache})
	r, err := gitops.New(t.Context(), gitops.Config{
		WorkDir: t.TempDir(), HeadRef: func(int) string { return "refs/heads/main" },
		Remote:     func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil },
		GCInterval: -1, GCThresholdBytes: 1, Observe: observeGitCacheOperations(nil, cache),
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := r.AcquireCommits(t.Context(), forge.RepoRef{Path: "owner/repo"}, []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	snapshot := awaitGitCache(t, h, func(s status.GitCacheSnapshot) bool {
		return s.Repositories["owner/repo"].GCOutcomes[gitops.GCFailed] == 1
	})
	repo := snapshot.Repositories["owner/repo"]
	if repo.LastGCAt == nil || repo.LastCacheBytes != nil || repo.Operations["acquire-commits"].Count != 1 {
		t.Fatalf("failed GC not reflected accurately: %+v", repo)
	}
}

type blockedGitCacheLog struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (*blockedGitCacheLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockedGitCacheLog) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.entered); <-h.release })
	return nil
}
func (h *blockedGitCacheLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockedGitCacheLog) WithGroup(string) slog.Handler      { return h }

func TestGitCacheRealObserverDrops(t *testing.T) {
	remote, sha := gitCacheFixture(t)
	keys := []store.QueueKey{{Repository: "owner/repo"}}
	cache := status.NewGitCache(func() []store.QueueKey { return keys })
	h := status.Handler(nil, keys, "fixture-read-token", nil, status.ReadOptions{GitCache: cache})
	logs := &blockedGitCacheLog{entered: make(chan struct{}), release: make(chan struct{})}
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(logs.release) }) })
	r, err := gitops.New(t.Context(), gitops.Config{
		WorkDir: t.TempDir(), HeadRef: func(int) string { return "refs/heads/main" },
		Remote:     func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil },
		GCInterval: -1, GCThresholdBytes: -1, Observe: observeGitCacheOperations(slog.New(logs), cache),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := forge.RepoRef{Path: "owner/repo"}
	release, err := r.AcquireCommits(t.Context(), repo, []string{sha})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	select {
	case <-logs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observer did not reach logging")
	}
	const operations = 300
	done := make(chan error, 1)
	go func() {
		for range operations {
			if _, err := r.ReadCommitMessage(t.Context(), repo, sha); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked observer stalled Git operations")
	}
	unblock.Do(func() { close(logs.release) })
	snapshot := awaitGitCache(t, h, func(s status.GitCacheSnapshot) bool {
		repo := s.Repositories["owner/repo"]
		return repo.Operations["read-commit-message"].Count+repo.Dropped == operations
	})
	observed := snapshot.Repositories["owner/repo"]
	if observed.Dropped != operations-256 || observed.Operations["read-commit-message"].Count != 256 {
		t.Fatalf("bounded observer losses not exposed: %+v", observed)
	}
	t.Logf("real observer delivered %d local reads and reported %d dropped samples", observed.Operations["read-commit-message"].Count, observed.Dropped)
}
