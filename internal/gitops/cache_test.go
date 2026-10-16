package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
				initial, fenceErr := f.g.EnsureFence(ctx, testRepo, testFence)
				must(t, fenceErr)
				tested := f.build(forge.Merge, prs, "cache-land")
				f.g = f.client(t, 0)
				_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested,
					Fence: testFence, ExpectedFence: initial, NextFenceID: "land/cache", PRs: prs})
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
					PRs: prs, Method: forge.Merge, BatchID: "cache"})
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
		PRs: f.prs[:1], Method: forge.Merge, BatchID: "cached"})
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
