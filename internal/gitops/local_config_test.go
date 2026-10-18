package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLocalConfigNeverHydratesMissingObjects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	data := "version: 1\nqueues:\n  - name: default\n"
	commit := f.commit(f.base, "pinned config", map[string]string{".openmerge.yml": data})
	f.push("main", commit, false)
	// The read must fail while the object exists only on the remote.
	if _, err := f.g.LocalConfig(ctx, testRepo, commit); err == nil {
		t.Fatal("read hydrated remote commit")
	}
	_, err := f.g.GoverningFiles(ctx, testRepo, forge.GoverningFilesRequest{CommitSHA: commit})
	must(t, err)
	// Normal ingestion populates the cache; the read may now use it with no
	// credentials, transport, mutable branch resolution or remote callback.
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		t.Fatal("read resolved remote")
		return "", Auth{}, nil
	}
	// Move the mutable remote ref; the local immutable pin must still win.
	moved := f.commit(commit, "moved config", map[string]string{".openmerge.yml": "unrelated moved policy"})
	f.push("main", moved, false)
	got, err := f.g.LocalConfig(ctx, testRepo, commit)
	if err != nil || string(got) != data {
		t.Fatalf("local read: %q %v", got, err)
	}
	if _, err := f.g.LocalConfig(ctx, testRepo, strings.Repeat("9", 40)); err == nil {
		t.Fatal("missing local commit accepted")
	}
	if _, err := f.g.LocalConfig(ctx, testRepo, "main"); err == nil {
		t.Fatal("mutable ref accepted")
	}
}

func TestLocalConfigBusyCacheReturnsWithoutWaiting(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.g.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(testRepo.Path))))
	lock, _ := cacheLocks.LoadOrStore(dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.g.LocalConfig(ctx, testRepo, strings.Repeat("a", 40)); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("busy cache accepted")
		}
	case <-ctx.Done():
		t.Fatal("local read waited for shared cache lock")
	}
}
