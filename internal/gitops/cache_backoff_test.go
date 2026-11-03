package gitops

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCacheGCBackoff(t *testing.T) {
	for _, outcome := range []GCOutcome{GCTimedOut, GCFailed} {
		for _, interval := range []time.Duration{2 * time.Hour, -1} {
			t.Run(string(outcome)+"/"+interval.String(), func(t *testing.T) {
				f := newFixture(t)
				f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
				f.build(forge.Merge, f.prs[:1], "backoff")
				s, unlock, err := f.g.open(t.Context(), testRepo, "test")
				must(t, err)
				unlock()
				stamp := filepath.Join(s.dir, "openmerge-gc")
				must(t, os.WriteFile(stamp, []byte("0\n"), 0o600))
				old := time.Now().Add(-24 * time.Hour)
				must(t, os.Chtimes(stamp, old, old))
				f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = interval, 1
				f.g.maintenanceTimeout = 500 * time.Millisecond
				gitPath := f.g.git
				wrapper := filepath.Join(t.TempDir(), "git")
				action := "exit 42"
				if outcome == GCTimedOut {
					action = "exec sleep 10"
				}
				writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = gc ]; then\n"+action+"\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
				f.g.git = wrapper
				samples := make(chan OperationStats, 8)
				f.g.cfg.Observe = func(s OperationStats) { samples <- s }
				f.build(forge.Merge, f.prs[:1], "backoff")
				failed := nextOperation(t, samples, "build-test-ref")
				if !failed.GCRan || failed.GCOutcome != outcome || failed.GC <= 0 || failed.CacheBytesMeasured || failed.Maintenance < failed.GC {
					t.Fatalf("unexpected failed GC stats: %+v", failed)
				}
				if outcome == GCTimedOut && (failed.GC < 300*time.Millisecond || failed.Hold >= failed.GC) {
					t.Fatalf("hold includes maintenance or deadline was not exercised: %+v", failed)
				}
				attempt := filepath.Join(s.dir, "openmerge-gc-attempt")
				info, err := os.Stat(attempt)
				must(t, err)
				if time.Since(info.ModTime()) > 5*time.Second {
					t.Fatal("failure stamp did not record the attempt time")
				}
				if data, err := os.ReadFile(stamp); err != nil || string(data) != "0\n" {
					t.Fatalf("failed GC rewrote success stamp: %q %v", data, err)
				}
				f.g.git, f.g.maintenanceTimeout = gitPath, 0
				// A new Repo simulates restart and must honor the persisted failure.
				cfg := f.g.cfg
				f.g, err = New(t.Context(), cfg)
				must(t, err)
				f.base = f.commit(f.base, "new base during backoff", map[string]string{"next": "base"})
				f.push("main", f.base, false)
				gitCommand(t, s.dir, "", nil, "update-ref", "refs/openmerge-cache/pr/stale", f.heads[1])
				gitCommand(t, s.dir, "", nil, "update-ref", acquiredRefPrefix+"dead/0", f.heads[1])
				// Test the one-hour floor as well as the configured interval.
				backoff := interval
				if backoff <= 0 {
					backoff = time.Hour
				}
				recent := time.Now().Add(-backoff / 2)
				must(t, os.Chtimes(attempt, recent, recent))
				f.build(forge.Merge, f.prs[:1], "backoff")
				skipped := nextOperation(t, samples, "build-test-ref")
				if skipped.GCRan || skipped.GC != 0 || skipped.GCOutcome != GCSkippedBackoff || skipped.Maintenance <= 0 {
					t.Fatalf("GC did not back off: %+v", skipped)
				}
				if got := gitCommand(t, s.dir, "", nil, "rev-parse", baseRef); got != f.base {
					t.Fatalf("base ref during backoff = %s, want %s", got, f.base)
				}
				for _, prefix := range []string{"refs/openmerge-cache/pr/", acquiredRefPrefix} {
					if refs := gitCommand(t, s.dir, "", nil, "for-each-ref", "--format=%(refname)", prefix); refs != "" {
						t.Fatalf("backoff prevented ref sweep: %s", refs)
					}
				}
				expired := time.Now().Add(-backoff - time.Minute)
				must(t, os.Chtimes(attempt, expired, expired))
				f.build(forge.Merge, f.prs[:1], "backoff")
				success := nextOperation(t, samples, "build-test-ref")
				if !success.GCRan || success.GCOutcome != GCOK || success.GC <= 0 || !success.CacheBytesMeasured || success.CacheBytes <= 0 {
					t.Fatalf("GC did not resume: %+v", success)
				}
				if _, err := os.Stat(attempt); !os.IsNotExist(err) {
					t.Fatalf("successful GC retained backoff: %v", err)
				}
				if data, err := os.ReadFile(stamp); err != nil || strings.TrimSpace(string(data)) != strconv.FormatInt(success.CacheBytes, 10) {
					t.Fatalf("post-GC size not recorded: %q %v", data, err)
				}
			})
		}
	}
}

func TestCacheKilledGCKeepsCacheUsable(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
	data := make([]byte, 16<<20)
	_, err := rand.Read(data)
	must(t, err)
	f.base = f.commit(f.base, "large base", map[string]string{"large.bin": string(data)})
	f.push("main", f.base, false)
	f.build(forge.Merge, f.prs[:1], "killed-gc")
	s, unlock, err := f.g.open(t.Context(), testRepo, "test")
	must(t, err)
	unlock()
	gitCommand(t, s.dir, "", nil, "config", "pack.compression", "0")
	gitPath, err := exec.LookPath("git")
	must(t, err)
	helpers := t.TempDir()
	record := filepath.Join(t.TempDir(), "interrupted-pack")
	// Intercept Git's child dispatch, kill real pack-objects only once it has
	// created a real temporary pack, then keep GC waiting past its deadline.
	writeExecutable(t, filepath.Join(helpers, "git"), `if [ "$1" = pack-objects ]; then
`+shellQuote(gitPath)+` "$@" &
child=$!
while kill -0 "$child" 2>/dev/null; do
  for file in "$GIT_DIR"/objects/pack/tmp_pack_*; do
    if [ -f "$file" ]; then
      kill -KILL "$child"
      wait "$child"
      printf '%s\n' "$file" > `+shellQuote(record)+`
      sleep 1
      exit 42
    fi
  done
done
wait "$child"
exit $?
fi
exec `+shellQuote(gitPath)+` "$@"
`)
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = gc ]; then\nexec "+shellQuote(gitPath)+" --exec-path="+shellQuote(helpers)+" \"$@\"\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper
	f.g.maintenanceTimeout = 500 * time.Millisecond
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = time.Hour, 1
	samples := make(chan OperationStats, 8)
	f.g.cfg.Observe = func(s OperationStats) { samples <- s }
	f.build(forge.Merge, f.prs[:1], "killed-gc")
	if stats := nextOperation(t, samples, "build-test-ref"); stats.GCOutcome != GCTimedOut || !stats.GCRan {
		t.Fatalf("real GC was not killed at deadline: %+v", stats)
	}
	tmp, err := os.ReadFile(record)
	must(t, err)
	tmpPath := strings.TrimSpace(string(tmp))
	_, err = os.Stat(tmpPath)
	must(t, err)
	_, err = os.Stat(filepath.Join(s.dir, "gc.pid"))
	must(t, err)
	f.g.git, f.g.maintenanceTimeout = gitPath, 0
	// Force a fresh fetch as well as a build while the leftovers still exist.
	f.heads[1] = f.commit(f.heads[1], "new head after killed GC", map[string]string{"after": "interruption"})
	f.prs[0].HeadSHA = f.heads[1]
	gitCommand(t, f.source, "", nil, "push", f.remote, f.heads[1]+":"+headRef(1))
	f.build(forge.Merge, f.prs[:1], "after-killed-gc")
	if stats := nextOperation(t, samples, "build-test-ref"); stats.GCOutcome != GCSkippedBackoff {
		t.Fatalf("next build did not honor backoff: %+v", stats)
	}
	gitCommand(t, s.dir, "", nil, "fsck", "--no-dangling")
	attempt := filepath.Join(s.dir, "openmerge-gc-attempt")
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(attempt, old, old))
	f.build(forge.Merge, f.prs[:1], "recovered-gc")
	if stats := nextOperation(t, samples, "build-test-ref"); stats.GCOutcome != GCOK {
		t.Fatalf("Git did not recover stale pid and temporary pack: %+v", stats)
	}
	for _, path := range []string{tmpPath, filepath.Join(s.dir, "gc.pid"), attempt} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Git left %s after successful retry: %v", filepath.Base(path), err)
		}
	}
	gitCommand(t, s.dir, "", nil, "fsck", "--no-dangling")
}

func TestCacheGCBackoffIgnoresFutureAttempt(t *testing.T) {
	for _, trigger := range []string{"interval", "size"} {
		t.Run(trigger, func(t *testing.T) {
			f := newFixture(t)
			f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
			f.build(forge.Merge, f.prs[:1], "clock-skew")
			s, unlock, err := f.g.open(t.Context(), testRepo, "clock-skew")
			must(t, err)
			defer unlock()
			if trigger == "interval" {
				f.g.cfg.GCInterval = time.Hour
			} else {
				f.g.cfg.GCThresholdBytes = 1
			}
			stamp := filepath.Join(s.dir, "openmerge-gc")
			must(t, os.WriteFile(stamp, []byte("0\n"), 0o600))
			old := time.Now().Add(-24 * time.Hour)
			must(t, os.Chtimes(stamp, old, old))
			attempt := filepath.Join(s.dir, "openmerge-gc-attempt")
			must(t, os.WriteFile(attempt, nil, 0o600))
			future := time.Now().AddDate(5, 0, 0)
			must(t, os.Chtimes(attempt, future, future))
			var stats OperationStats
			must(t, s.maintainCache(t.Context(), &stats))
			if !stats.GCRan || stats.GCOutcome != GCOK {
				t.Fatalf("future attempt suppressed due GC: %+v", stats)
			}
			if _, err := os.Stat(attempt); !os.IsNotExist(err) {
				t.Fatalf("successful GC retained future attempt: %v", err)
			}
		})
	}
}
