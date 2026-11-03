package gitops

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestReviewLeaderExitDescendantMutation(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
	f.build(forge.Merge, f.prs[:1], "leader-exit")
	s, unlock, err := f.g.open(t.Context(), testRepo, "leader-exit")
	must(t, err)
	release := filepath.Join(t.TempDir(), "release")
	marker := filepath.Join(s.dir, "mutation-after-unlock")
	wrapper := filepath.Join(t.TempDir(), "git")
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	script := "( while [ ! -e " + shellQuote(release) + " ]; do sleep 0.01; done; " + shellQuote(f.g.git) + " --git-dir=" + shellQuote(s.dir) + " update-ref refs/openmerge-cache/late-review " + shellQuote(f.base) + "; printf mutation > " + shellQuote(marker) + "; sleep 1 ) &\nprintf '%s' \"$!\" > " + shellQuote(pidFile) + "\n"
	// Bound the child even if the test fails early.
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = gc ]; then\n"+script+"exit 1\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
	f.g.git = wrapper
	f.g.maintenanceTimeout = 100 * time.Millisecond
	f.g.cfg.GCThresholdBytes = 1
	started := time.Now()
	unlock()
	lock, _ := cacheLocks.Load(s.dir)
	mu := lock.(*sync.Mutex)
	if !mu.TryLock() {
		t.Fatal("cache not released")
	}
	defer mu.Unlock()
	t.Logf("maintenance returned and next owner acquired lock after %s", time.Since(started))
	_, err = os.Stat(filepath.Join(s.dir, "openmerge-gc-attempt"))
	must(t, err)
	assertChildDead(t, readChildPID(t, pidFile))
	must(t, os.WriteFile(release, nil, 0600))
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			ref, _ := os.ReadFile(filepath.Join(s.dir, "refs/openmerge-cache/late-review"))
			t.Fatalf("old descendant mutated cache while next owner holds lock: %s, git ref=%s", data, ref)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReviewObserverIdleRestartStress(t *testing.T) {
	var active, overlap atomic.Int32
	delivered := make(chan OperationStats, 1)
	r := &Repo{cfg: Config{Observe: func(s OperationStats) {
		if active.Add(1) != 1 {
			overlap.Add(1)
		}
		delivered <- s
		active.Add(-1)
	}}}
	for range 20000 {
		r.observeOperation(OperationStats{})
		select {
		case s := <-delivered:
			if s.Dropped != 0 {
				t.Fatal("unexpected drop")
			}
		case <-time.After(time.Second):
			t.Fatal("stranded sample")
		}
	}
	if overlap.Load() != 0 {
		t.Fatalf("concurrent callbacks: %d", overlap.Load())
	}
}
