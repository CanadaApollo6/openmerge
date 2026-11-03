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

func TestRevisionDiffHunkProcessReleasesCache(t *testing.T) {
	for _, mode := range []string{"cancellation", "early-exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			// Resolve the actual cache mutex before PATH intercepts only the
			// transitive forge command. Repo.git already names the real Git.
			s, unlock, err := f.g.openSession(t.Context(), testRepo, "hunk-process-test", true)
			must(t, err)
			unlock()
			lock, ok := cacheLocks.Load(s.dir)
			if !ok {
				t.Fatal("cache mutex missing")
			}
			mu := lock.(*sync.Mutex)
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "descendant")
			ending := "exit \"$status\"\n"
			if mode == "cancellation" {
				ending = "wait\n"
			}
			writeExecutable(t, filepath.Join(dir, "git"), "for a in \"$@\"; do\n"+
				"if [ \"$a\" = --no-index ]; then\n"+
				shellQuote(f.g.git)+" \"$@\"\nstatus=$?\n"+
				"sleep 30 &\nprintf '%s\\n' \"$!\" > "+shellQuote(pidFile)+"\n"+
				ending+"fi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

			ctx, cancel := context.WithCancel(t.Context())
			type outcome struct {
				diff forge.RevisionDiff
				err  error
			}
			done := make(chan outcome, 1)
			returned := false
			t.Cleanup(func() {
				cancel()
				// Also settle a raw-exec mutant before temporary directories go away.
				if data, err := os.ReadFile(pidFile); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
				if !returned {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("RevisionDiff did not settle after test cleanup")
					}
				}
			})
			go func() {
				diff, err := f.g.RevisionDiff(ctx, testRepo, forge.RevisionDiffRequest{BaseSHA: f.base, HeadSHA: f.prs[0].HeadSHA})
				done <- outcome{diff, err}
			}()
			ready, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			pid := 0
			for pid == 0 {
				data, err := os.ReadFile(pidFile)
				if err == nil && strings.HasSuffix(string(data), "\n") {
					pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
					must(t, err)
					break
				}
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				select {
				case <-ready.Done():
					t.Fatal("hunk descendant did not start")
				case <-time.After(time.Millisecond):
				}
			}
			started := time.Now()
			if mode == "cancellation" {
				if mu.TryLock() {
					mu.Unlock()
					t.Fatal("RevisionDiff did not hold the cache mutex during hunk collection")
				}
				cancel()
			}
			select {
			case got := <-done:
				returned = true
				if mode == "cancellation" {
					if !errors.Is(got.err, context.Canceled) || got.diff.Hunks.Completeness != forge.DiffIncomplete {
						t.Errorf("canceled hunk result: %+v, err=%v", got.diff.Hunks, got.err)
					}
				} else {
					must(t, got.err)
					if got.diff.Hunks.Completeness != forge.DiffComplete || len(got.diff.Files) != 1 || len(got.diff.Files[0].Hunks) != 1 {
						t.Errorf("exit-code-1 patch result: %+v", got.diff)
					}
				}
				t.Logf("%s returned after %s", mode, time.Since(started))
			case <-time.After(2 * time.Second):
				t.Error("RevisionDiff retained inherited pipes beyond the return bound")
			}
			// Check both properties even when the return bound fails, so a
			// mutation shows the lock and live-process consequences too.
			if !mu.TryLock() {
				t.Error("RevisionDiff retained the cache mutex")
			} else {
				mu.Unlock()
			}
			assertChildDead(t, pid)
		})
	}
}
