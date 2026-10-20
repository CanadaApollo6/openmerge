package gitops

// Permanent regressions ported from GPT-6 Astra's second review round.

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

type r2Driver struct {
	forge.Driver
	closed []int
}

func (d *r2Driver) CheckRuns(context.Context, forge.RepoRef, string) ([]forge.CheckRun, error) {
	return nil, nil
}

func (d *r2Driver) MarkLanded(_ context.Context, _ forge.RepoRef, pr int, _ string) error {
	d.closed = append(d.closed, pr)
	return nil
}

// r2LostLandResponse loses the successful push response before the executor can
// verify the base or record Landed, forcing recovery through the remote fence.
type r2LostLandResponse struct {
	forge.Git
	lost error
}

func (g *r2LostLandResponse) Land(ctx context.Context, repo forge.RepoRef, req forge.LandRequest, opts forge.PushOptions) (string, error) {
	if _, err := g.Git.Land(ctx, repo, req, opts); err != nil {
		return "", err
	}
	return "", g.lost
}

func TestR2ExternalRollbackFailsSafe(t *testing.T) {
	for _, recoverLost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-response=%v", recoverLost), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
			must(t, err)
			tested := f.build(forge.Squash, f.prs[:1], "r2-external")
			gitPath, err := exec.LookPath("git")
			must(t, err)
			must(t, os.MkdirAll(filepath.Join(f.remote, "hooks"), 0700))
			marker := filepath.Join(t.TempDir(), "receiver-updates")
			writeExecutable(t, filepath.Join(f.remote, "hooks", "pre-receive"), "cat > "+shellQuote(marker)+"\nunset GIT_QUARANTINE_PATH GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES\n"+shellQuote(gitPath)+" --git-dir="+shellQuote(f.remote)+" update-ref refs/heads/main "+f.base+" "+tested+"\n")
			wrapper := filepath.Join(t.TempDir(), "git")
			writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = push ]; then\n"+shellQuote(gitPath)+" --git-dir="+shellQuote(f.remote)+" update-ref refs/heads/main "+tested+" "+f.base+" || exit 90\nbreak\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
			f.g.git = wrapper
			prs := []queue.BatchPR{{PR: 1, HeadSHA: f.prs[0].HeadSHA}}
			s := queue.State{BaseBranch: "main", BaseSHA: f.base, Fence: initial, Entries: []queue.Entry{{PR: 1, HeadSHA: f.prs[0].HeadSHA}}, Active: &queue.Batch{ID: "r2", Phase: queue.Testing, BaseSHA: f.base, TestedSHA: tested, Method: queue.Squash, PRs: prs}}
			d := &r2Driver{}
			x := executor.Executor{Git: f.g, Driver: d, Repo: testRepo, Base: "main", AllowNoRequiredChecks: true, NewID: func() (string, error) { return "r2", nil }}
			lost := errors.New("lost Land response")
			if recoverLost {
				x.Git = &r2LostLandResponse{Git: f.g, lost: lost}
			}
			landed := 0
			emit := func(e queue.Event) error {
				if _, ok := e.(queue.Landed); ok {
					landed++
				}
				s = queue.Apply(s, e)
				return nil
			}
			requireAbsent := func(err error, attempt string) {
				t.Helper()
				var absent *executor.ErrLandingNotOnBase
				if !errors.As(err, &absent) || absent.TestedSHA != tested || absent.BaseTip != f.base || absent.AttemptID != attempt {
					t.Fatalf("missing landing verification diagnostics: %v", err)
				}
				for _, detail := range []string{tested, f.base, attempt} {
					if !strings.Contains(err.Error(), detail) {
						t.Fatalf("error %q omits %q", err, detail)
					}
				}
			}
			err = x.Execute(ctx, queue.Land{BatchID: "r2", ExpectedBaseSHA: f.base, TestedSHA: tested, FenceToken: initial, PRs: prs}, emit)
			if recoverLost {
				if !errors.Is(err, lost) {
					t.Fatalf("did not lose the successful push response: %v", err)
				}
			} else {
				requireAbsent(err, "r2")
			}
			updates, err := os.ReadFile(marker)
			must(t, err)
			if strings.Contains(string(updates), " refs/heads/main") {
				t.Fatal("reproduction did not skip the base update")
			}
			fence, id, fenceTested, err := f.g.ReadFence(ctx, testRepo, testFence)
			must(t, err)
			if fence == initial || id != "land/r2" || fenceTested != tested || f.read("main") != f.base {
				t.Fatal("reproduction did not advance only the fence after rollback")
			}
			for range 3 {
				requireAbsent(x.Execute(ctx, queue.ResolveLanding{BatchID: "r2", AttemptID: "r2", TestedSHA: tested, FenceToken: initial}, emit), "r2")
			}
			if landed != 0 || s.BaseSHA != f.base || s.Active == nil || s.Active.Phase != queue.Landing {
				t.Fatalf("rollback recorded a landing: landed=%d state=%+v", landed, s)
			}
			for _, a := range queue.Plan(queue.Config{MergeMethod: queue.Squash, ResolveAfter: time.Minute}, s, epoch.Add(2*time.Minute)) {
				if _, ok := a.(queue.MarkLanded); ok {
					t.Fatal("unverified landing planned PR closure")
				}
			}
			// Even a stale closing action must independently refuse to call the driver.
			requireAbsent(x.Execute(ctx, queue.MarkLanded{PR: 1, HeadSHA: prs[0].HeadSHA, LandedSHA: tested}, emit), "")
			if len(d.closed) != 0 || landed != 0 {
				t.Fatalf("rollback caused Landed=%d or MarkLanded=%v", landed, d.closed)
			}
		})
	}
}

func TestR2NonceAndCraftedMessages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
	must(t, err)
	s, unlock, err := f.g.open(ctx, testRepo)
	must(t, err)
	seen := map[string]bool{}
	for i := 0; i < 24; i++ {
		for _, id := range []string{"land/probe", "abandon/probe"} {
			tested := ""
			if strings.HasPrefix(id, "land/") {
				tested = f.base
			}
			sha, err := s.fence(ctx, initial, id, tested)
			must(t, err)
			msg, err := s.command(ctx, "", "show", "-s", "--format=%B", sha)
			must(t, err)
			_, nonce, ok := strings.Cut(msg, " nonce=")
			decoded, err := hex.DecodeString(nonce)
			if !ok || err != nil || len(decoded) != 16 || seen[sha] {
				t.Fatalf("bad/repeated nonce: %q", msg)
			}
			seen[sha] = true
		}
	}
	unlock()
	for _, msg := range []string{
		"openmerge fence land/probe tested=" + f.base + " nonce=not-hex tested=" + f.heads[1],
		"openmerge fence land/probe tested=" + f.base + " nonce=",
		"openmerge fence land/probe nonce=abcd tested=" + f.base,
		"openmerge fence abandon/probe nonce=abcd\nopenmerge fence land/other tested=" + f.base,
		" openmerge fence land/probe tested=" + f.base + " nonce=" + strings.Repeat("a", 32),
		"openmerge fence land/probe tested=" + f.base + " nonce=" + strings.Repeat("a", 32) + " ",
		"openmerge fence land/probe tested=" + f.base + " nonce=" + strings.Repeat("a", 32) + "\n",
		"openmerge fence land/probe tested=" + f.base + " nonce=" + strings.Repeat("a", 32) + " tested=" + f.base,
		"openmerge fence land/probe tested=" + f.base + " nonce=" + strings.Repeat("a", 32) + " nonce=" + strings.Repeat("b", 32),
	} {
		sha := f.commit(f.base, msg, nil)
		gitCommand(t, f.source, "", nil, "push", "--force", f.remote, sha+":refs/heads/"+testFence)
		_, id, tested, err := f.g.ReadFence(ctx, testRepo, testFence)
		must(t, err)
		if id != "" || tested != "" {
			t.Fatalf("malformed fence recognized: message=%q id=%q tested=%q", msg, id, tested)
		}
		x := executor.Executor{Git: f.g, Repo: testRepo, Base: "main", AllowNoRequiredChecks: true}
		events := 0
		err = x.Execute(ctx, queue.ResolveLanding{BatchID: "probe", AttemptID: "probe", TestedSHA: f.base, FenceToken: initial}, func(queue.Event) error { events++; return nil })
		var tampered *executor.ErrFenceTampered
		if !errors.As(err, &tampered) || events != 0 {
			t.Fatalf("malformed fence settled a landing: %v, events=%d", err, events)
		}
	}
}

func TestR2AlreadyTestedNeverPushes(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(fmt.Sprint(matching), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
			must(t, err)
			tested := f.build(forge.Merge, f.prs[:1], "already")
			gitCommand(t, f.remote, "", nil, "update-ref", "refs/heads/main", tested, f.base)
			gitPath, err := exec.LookPath("git")
			must(t, err)
			marker := filepath.Join(t.TempDir(), "pushed")
			wrapper := filepath.Join(t.TempDir(), "git")
			writeExecutable(t, wrapper, "for a in \"$@\"; do\nif [ \"$a\" = push ]; then\ntouch "+shellQuote(marker)+"\nexit 90\nfi\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
			f.g.git = wrapper
			expected := f.base
			if matching {
				expected = tested
			}
			_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: expected, TestedSHA: tested, Fence: testFence, ExpectedFence: initial, NextFenceID: "land/already", PRs: f.prs[:1]}, forge.PushOptions{})
			if !errors.Is(err, forge.ErrLeaseRejected) {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("push invoked")
			}
			if f.read(testFence) != initial {
				t.Fatal("changed fence")
			}
		})
	}
}

func TestR2GCConcurrentSameCache(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.g.cfg.GCThresholdBytes = 1
	other, err := New(ctx, f.g.cfg)
	must(t, err)
	gitPath, err := exec.LookPath("git")
	must(t, err)
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	release := filepath.Join(dir, "release")
	record := filepath.Join(dir, "gc")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	wrapper := filepath.Join(dir, "git")
	writeExecutable(t, wrapper, "for a in \"$@\"; do\ncase \"$a\" in\npush) touch "+shellQuote(blocked)+"; while [ ! -e "+shellQuote(release)+" ]; do sleep 0.01; done ;;\ngc) printf gc >> "+shellQuote(record)+" ;;\nesac\ndone\nexec "+shellQuote(gitPath)+" \"$@\"\n")
	f.g.git = wrapper
	other.git = wrapper
	first := make(chan error, 1)
	go func() {
		_, err := f.g.BuildTestRef(ctx, testRepo, forge.BuildRequest{Ref: "openmerge/queue/gc-concurrent", BaseSHA: f.base, PRs: f.prs[:1], Method: forge.Merge, BatchID: "gc-concurrent"}, forge.PushOptions{})
		first <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(blocked); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("push did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	second := make(chan error, 1)
	go func() { _, err := other.ReadRef(ctx, testRepo, "main"); second <- err }()
	select {
	case err := <-second:
		t.Fatalf("other operation escaped lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("GC ran with push held")
	}
	must(t, os.WriteFile(release, nil, 0600))
	must(t, <-first)
	must(t, <-second)
	if _, err := os.Stat(record); err != nil {
		t.Fatal("GC never ran", err)
	}
}

func TestR2RefreshFailureScrubsBothTokens(t *testing.T) {
	for _, operation := range []string{"land", "advance"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			initial, err := f.g.EnsureFence(ctx, testRepo, testFence, forge.PushOptions{})
			must(t, err)
			tested := f.build(forge.Merge, f.prs[:1], "refresh-redact")
			calls := 0
			f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
				calls++
				if calls == 1 {
					return f.remote, Auth{Password: "old-secret"}, nil
				}
				return "", Auth{Password: "fresh-secret"}, fmt.Errorf("old-secret fresh-secret: %w", ErrAuth)
			}
			if operation == "land" {
				_, err = f.g.Land(ctx, testRepo, forge.LandRequest{Base: "main", ExpectedBaseSHA: f.base, TestedSHA: tested, Fence: testFence, ExpectedFence: initial, NextFenceID: "land/refresh-redact", PRs: f.prs[:1]}, forge.PushOptions{})
			} else {
				_, err = f.g.AdvanceFence(ctx, testRepo, testFence, initial, "abandon/refresh-redact", forge.PushOptions{})
			}
			if calls != 2 || !errors.Is(err, ErrAuth) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if strings.Contains(err.Error(), "old-secret") || strings.Contains(err.Error(), "fresh-secret") {
				t.Fatalf("token leaked: %v", err)
			}
			actual := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/main")
			fence := gitCommand(t, f.remote, "", nil, "rev-parse", "refs/heads/"+testFence)
			if actual != f.base || fence != initial {
				t.Fatal("refs changed on refresh failure")
			}
		})
	}
}
