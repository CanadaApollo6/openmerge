package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func inheritedPipeChild(t *testing.T) (script, pidFile string) {
	t.Helper()
	pidFile = filepath.Join(t.TempDir(), "child-pid")
	script = "sleep 3 &\nprintf '%s' \"$!\" > " + shellQuote(pidFile) + "\n"
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(data)); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return script, pidFile
}

func TestCommandWaitDelayBoundsInheritedPipes(t *testing.T) {
	f := newFixture(t)
	script, pidFile := inheritedPipeChild(t)
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, script+"exit 0\n")
	f.g.git = wrapper
	s := &session{r: f.g}
	started := time.Now()
	_, _, err := s.run(t.Context(), "", "--version")
	elapsed := time.Since(started)
	t.Logf("inherited pipes returned after %s: %v", elapsed, err)
	if err != nil || elapsed > 2*time.Second {
		t.Fatalf("unbounded pipe drain: elapsed=%s, err=%v", elapsed, err)
	}
	assertChildDead(t, readChildPID(t, pidFile))
}

func TestMaintenanceDeadlineKillsDescendants(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
	f.build(forge.Merge, f.prs[:1], "deadline")
	s, unlock, err := f.g.open(t.Context(), testRepo, "deadline")
	must(t, err)
	script, pidFile := inheritedPipeChild(t)
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = gc ]; then\n"+script+"wait\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
	f.g.git = wrapper
	f.g.maintenanceTimeout = 100 * time.Millisecond
	f.g.cfg.GCThresholdBytes = 1
	started := time.Now()
	unlock()
	elapsed := time.Since(started)
	t.Logf("maintenance returned after %s for a 100ms deadline", elapsed)
	if elapsed > 1600*time.Millisecond {
		t.Errorf("maintenance exceeded deadline plus pipe drain allowance: %s", elapsed)
	}
	lock, _ := cacheLocks.Load(s.dir)
	mu := lock.(*sync.Mutex)
	if !mu.TryLock() {
		t.Fatal("maintenance retained the cache lock")
	}
	mu.Unlock()
	_, err = os.Stat(filepath.Join(s.dir, "openmerge-gc-attempt"))
	must(t, err)
	data, err := os.ReadFile(pidFile)
	must(t, err)
	pid, err := strconv.Atoi(string(data))
	must(t, err)
	assertChildDead(t, pid)
}

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	must(t, err)
	pid, err := strconv.Atoi(string(data))
	must(t, err)
	return pid
}

func processState(pid int) (string, error) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	_, fields, ok := strings.Cut(string(stat), ") ")
	if !ok {
		return "", errors.New("invalid process stat")
	}
	return strings.Fields(fields)[0], nil
}

func assertChildDead(t *testing.T, pid int) {
	t.Helper()
	// A killed orphan can remain a zombie until init reaps it. Poll the actual
	// process state; elapsed time alone never establishes descendant death.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for {
		state, err := processState(pid)
		if os.IsNotExist(err) || err == nil && state == "Z" {
			return
		}
		must(t, err)
		select {
		case <-ctx.Done():
			t.Fatalf("descendant %d remains alive after command return: %s", pid, state)
		case <-time.After(time.Millisecond):
		}
	}
}
