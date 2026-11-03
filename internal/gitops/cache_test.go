package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestPRCacheRefsAreTemporary(t *testing.T) {
	for _, operation := range []string{"build", "land", "conflict", "changed-head", "failed-fetch"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			prs := f.prs[:1]
			var err error
			if operation == "land" {
				initial, fenceErr := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
				must(t, fenceErr)
				tested := f.build(forge.Merge, prs, "cache-land")
				f.g = f.client(t, 0)
				_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
					Fence: testFence, ExpectedFence: initial, NextFenceID: "land/cache", PRs: prs}, forge.PushOptions{})
			} else {
				switch operation {
				case "conflict":
					prs = []forge.BuildPR{{Number: 10, HeadSHA: f.heads[10]}, {Number: 11, HeadSHA: f.heads[11]}}
				case "changed-head":
					prs = []forge.BuildPR{{Number: 1, HeadSHA: f.heads[2]}}
				case "failed-fetch":
					gitPath, lookupErr := exec.LookPath("git")
					must(t, lookupErr)
					wrapper := filepath.Join(t.TempDir(), "git")
					writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = fetch ]; then\n"+shellQuote(gitPath)+" \"$@\" >/dev/null 2>&1 || exit 90\nexit 42\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
					f.g.git = wrapper
				}
				_, err = f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/cache", BaseSHA: f.base,
					PRs: prs, Method: forge.Merge, BatchID: "cache"}, forge.PushOptions{})
			}
			if operation == "build" || operation == "land" {
				must(t, err)
			} else if err == nil {
				t.Fatal("invalid operation succeeded")
			}
			s, unlock, err := f.g.open(ctx, testRepo)
			must(t, err)
			defer unlock()
			refs, err := s.command(ctx, "", "for-each-ref", "--format=%(refname)", "refs/openmerge-cache/pr/")
			must(t, err)
			if refs != "" {
				t.Fatalf("operation retained PR cache refs: %s", refs)
			}
		})
	}
}

func TestCachedHeadSkipsFetchButStillChecksRemotePin(t *testing.T) {
	f := newFixture(t)
	before := f.build(forge.Merge, f.prs[:1], "cached")
	gitPath, err := exec.LookPath("git")
	must(t, err)
	record := filepath.Join(t.TempDir(), "fetches")
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = fetch ]; then\nprintf fetch >> "+shellQuote(record)+"\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper
	if f.build(forge.Merge, f.prs[:1], "cached") != before {
		t.Fatal("cached build changed the tested commit")
	}
	head := f.commit(f.heads[1], "new PR head", map[string]string{"new.txt": "new"})
	gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(1))
	_, err = f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/cached", BaseSHA: f.base,
		PRs: f.prs[:1], Method: forge.Merge, BatchID: "cached"}, forge.PushOptions{})
	if err == nil || f.read("openmerge/queue/main/cached") != before {
		t.Fatal("cached object bypassed the remote PR pin check")
	}
	if data, err := os.ReadFile(record); !os.IsNotExist(err) {
		t.Fatalf("already-present heads were fetched again: %q, %v", data, err)
	}
}

func TestCacheGCPrunesUnreachableObjects(t *testing.T) {
	for _, trigger := range []string{"interval", "size"} {
		t.Run(trigger, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			s, unlock, err := f.g.open(ctx, testRepo)
			must(t, err)
			tree, err := s.command(ctx, "", "mktree")
			must(t, err)
			orphan, err := s.commit(ctx, tree, "unreachable cache commit")
			must(t, err)
			_, err = s.command(ctx, "", "update-ref", "refs/openmerge-cache/pr/999", orphan)
			must(t, err)
			stamp := filepath.Join(s.dir, "openmerge-gc")
			must(t, os.WriteFile(stamp, nil, 0o600))
			old := time.Now().Add(-2 * time.Hour)
			must(t, os.Chtimes(stamp, old, old))
			f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
			if trigger == "interval" {
				f.g.cfg.GCInterval = time.Hour
			} else {
				f.g.cfg.GCThresholdBytes = 1
			}
			unlock()
			if _, _, err := s.run(ctx, "", "cat-file", "-e", orphan); err == nil {
				t.Fatal("collection retained an unreachable PR cache object")
			}
			info, err := os.Stat(stamp)
			must(t, err)
			if !info.ModTime().After(old) {
				t.Fatal("successful collection did not advance its timestamp")
			}
		})
	}
}

func TestGCWaitsForPushToComplete(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCThresholdBytes = 1
	gitPath, err := exec.LookPath("git")
	must(t, err)
	record := filepath.Join(t.TempDir(), "commands")
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\ncase \"$a\" in\npush)\nprintf 'push-start\\n' >> "+shellQuote(record)+"\n"+shellQuote(gitPath)+" \"$@\" || exit 90\nprintf 'push-end\\n' >> "+shellQuote(record)+"\nexit 0\n;;\ngc) printf 'gc\\n' >> "+shellQuote(record)+" ;;\nesac\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper
	f.build(forge.Merge, f.prs[:1], "gc-push")
	data, err := os.ReadFile(record)
	must(t, err)
	if strings.TrimSpace(string(data)) != "push-start\npush-end\ngc" {
		t.Fatalf("collection overlapped the push: %q", data)
	}
}

func TestCacheGCKeepsBaseHistory(t *testing.T) {
	for _, operation := range []string{"build", "land"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			// Fetch an unrelated PR head while collection is off; it must not survive.
			f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
			f.build(forge.Merge, f.prs[1:2], "keep-other")
			f.g.cfg.GCThresholdBytes = 1
			retained := f.base
			if operation == "land" {
				initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
				must(t, err)
				tested := f.build(forge.Merge, f.prs[:1], "keep-land")
				_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
					Fence: testFence, ExpectedFence: initial, NextFenceID: "land/keep", PRs: f.prs[:1]}, forge.PushOptions{})
				must(t, err)
				retained = tested
			} else {
				f.build(forge.Merge, f.prs[:1], "keep-build")
			}
			s, unlock, err := f.g.open(ctx, testRepo)
			must(t, err)
			defer unlock()
			if _, err := s.command(ctx, "", "cat-file", "-e", retained+"^{commit}"); err != nil {
				t.Fatalf("collection dropped the retained base %s: %v", retained, err)
			}
			if _, err := s.command(ctx, "", "cat-file", "-e", f.heads[2]+"^{commit}"); err == nil {
				t.Fatal("collection kept an unrelated PR head")
			}
			got, err := s.command(ctx, "", "rev-parse", baseRef)
			must(t, err)
			if got != retained {
				t.Fatalf("%s = %s, want %s", baseRef, got, retained)
			}
		})
	}
}

func TestCacheGCSizeTriggerMeasuresGrowth(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, 1
	f.build(forge.Merge, f.prs[:1], "growth")
	ctx := context.Background()
	gitPath, err := exec.LookPath("git")
	must(t, err)
	record := filepath.Join(t.TempDir(), "gc")
	wrapper := filepath.Join(t.TempDir(), "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = gc ]; then\nprintf gc >> "+shellQuote(record)+"\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper

	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	stamp := filepath.Join(s.dir, "openmerge-gc")
	data, err := os.ReadFile(stamp)
	must(t, err)
	size, err := s.cacheSize(ctx)
	must(t, err)
	f.g.cfg.GCThresholdBytes = -1
	unlock()
	if size < 4 || strings.TrimSpace(string(data)) != strconv.FormatInt(size, 10) {
		t.Fatalf("stamp %q does not record the post-collection size %d", data, size)
	}
	for _, tc := range []struct {
		name     string
		baseline int64
		collect  bool
	}{
		// Over the threshold in total, but barely grown since the last collection.
		{"retained history", size - 1, false},
		{"growth", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			must(t, os.WriteFile(stamp, []byte(strconv.FormatInt(tc.baseline, 10)+"\n"), 0o600))
			f.g.cfg.GCThresholdBytes = size / 2
			_, unlock, err := f.g.open(ctx, testRepo)
			must(t, err)
			unlock()
			_, err = os.Stat(record)
			if collected := err == nil; collected != tc.collect {
				t.Fatalf("collected = %v, want %v (size %d, baseline %d, threshold %d)", collected, tc.collect, size, tc.baseline, size/2)
			}
		})
	}
}
