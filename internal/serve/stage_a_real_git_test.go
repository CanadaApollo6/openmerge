package serve

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Exercise the production gate's tested-message callback with queue-built real
// objects, then land those exact objects in an isolated local bare remote.
// Eligibility/persistence are covered by TestStageAReceiptGateAllMethods; this
// probe deliberately needs neither Postgres nor a GitHub driver.
func TestStageAGateRealGitAcquisition(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, restart := range []bool{false, true} {
			for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
				t.Run(fmt.Sprintf("depth-%d/restart-%t/%s", depth, restart, method), func(t *testing.T) {
					dir := t.TempDir()
					remote := filepath.Join(dir, "remote.git")
					git := func(input string, args ...string) string {
						t.Helper()
						cmd := exec.Command("git", args...)
						cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
						cmd.Stdin = strings.NewReader(input)
						out, err := cmd.CombinedOutput()
						if err != nil {
							t.Fatalf("git %v: %v: %s", args, err, out)
						}
						return strings.TrimSpace(string(out))
					}
					git("", "init", "--bare", "--template=", remote)
					tree := git("", "--git-dir="+remote, "mktree")
					base := git("base\n", "--git-dir="+remote, "commit-tree", tree)
					git("", "--git-dir="+remote, "update-ref", "refs/heads/main", base)
					key := store.QueueKey{Installation: "7", Repository: "local/first-parent", BaseBranch: "main"}
					prs := make([]forge.BuildPR, depth)
					pins := make([]queue.BatchPR, depth)
					for i := range prs {
						n := i + 1
						id, err := key.ChangeID(n)
						must(t, err)
						// Two source commits per member make rebase's composition longer than
						// the batch member count, unlike merge and squash.
						head := base
						for j := 0; j < 2; j++ {
							head = git(fmt.Sprintf("source %d/%d\n", n, j), "--git-dir="+remote, "commit-tree", tree, "-p", head)
						}
						git("", "--git-dir="+remote, "update-ref", fmt.Sprintf("refs/pull/%d/head", n), head)
						prs[i] = forge.BuildPR{Number: n, HeadSHA: head, Metadata: &forge.ChangeMetadata{ChangeID: string(id), Revision: 1}}
						pins[i] = queue.BatchPR{PR: n, HeadSHA: head}
					}
					cfg := gitops.Config{GCThresholdBytes: 1, WorkDir: filepath.Join(dir, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }}
					g, err := gitops.New(t.Context(), cfg)
					must(t, err)
					// Keep the base reachable locally, matching the sandbox: only the
					// generated tested commit should disappear during build GC.
					_, err = g.IsAncestor(t.Context(), forge.RepoRef{Path: key.Repository}, base, base)
					must(t, err)
					cache := filepath.Join(cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(key.Repository))))
					git("", "--git-dir="+cache, "fetch", "--no-tags", remote, base+":refs/heads/fixture-base")
					w := &worker{Target: Target{Key: key, Git: g}, stageA: &stageAPolicy{}}
					tested, err := g.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: "test-composition", BaseSHA: base, PRs: prs, Method: forge.MergeMethod(method), BatchID: "real-gate"})
					must(t, err)
					if restart {
						// Restart with no local objects: only the pushed remote survives.
						must(t, os.RemoveAll(cfg.WorkDir))
						g, err = gitops.New(t.Context(), cfg)
						must(t, err)
						w.Git = g
					}
					land := queue.Land{ExpectedBaseSHA: base, TestedSHA: tested, PRs: pins, Method: method}
					gate := (&Server{}).stageAGate(w, store.ClaimedAction{}, queue.Config{})
					messages, err := gate.ReadTestedMessages(t.Context(), land)
					must(t, err)
					if len(messages) != depth {
						t.Fatalf("messages=%d, want %d", len(messages), depth)
					}
					for i, message := range messages {
						metadata, err := forge.ParseChangeTrailers(message)
						must(t, err)
						if len(metadata) != 1 || metadata[0].ChangeID != prs[i].Metadata.ChangeID {
							t.Fatalf("member %d: %+v", i, metadata)
						}
					}
					// An extra queue-generated segment must fail closed at the
					// batch-derived limit, even though all objects are readable.
					extra, err := g.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: "extra-composition", BaseSHA: tested, PRs: prs[:1], Method: forge.MergeMethod(method), BatchID: "extra-gate"})
					must(t, err)
					overlong := land
					overlong.TestedSHA = extra
					if _, err := gate.ReadTestedMessages(t.Context(), overlong); err == nil || !strings.Contains(err.Error(), "tested member history exceeds composition limit") {
						t.Fatalf("overlong composition accepted: %v", err)
					}
					fence, err := g.EnsureFence(t.Context(), w.repo(), "test-fence")
					must(t, err)
					_, err = g.Land(t.Context(), w.repo(), forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: tested, Fence: "test-fence", ExpectedFence: fence, NextFenceID: "land/real-gate", PRs: prs})
					must(t, err)
					if got := git("", "--git-dir="+remote, "rev-parse", "refs/heads/main"); got != tested {
						t.Fatalf("landed %s, want %s", got, tested)
					}
				})
			}
		}
	}
}
