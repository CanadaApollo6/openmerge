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
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// Exercise the production gate's tested-message callback with queue-built real
// objects, then land those exact objects in an isolated local bare remote.
// Eligibility/persistence are covered by TestStageAReceiptGateAllMethods; this
// object probe needs no GitHub driver. With the test DB configured, it also
// persists the atomic start and reads the checks back after real-git settlement.
func TestStageAGateRealGitAcquisition(t *testing.T) {
	t.Parallel()
	for _, depth := range []int{1, 4} {
		for _, restart := range []bool{false, true} {
			for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase, queue.FastForward} {
				t.Run(fmt.Sprintf("depth-%d/restart-%t/%s", depth, restart, method), func(t *testing.T) {
					t.Parallel()
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
						if method == queue.FastForward && i > 0 {
							head = prs[i-1].HeadSHA
						}
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
					tested, err := g.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: "openmerge/queue/main/b1", BaseSHA: base, PRs: prs, Method: forge.MergeMethod(method), BatchID: "b1"}, forge.PushOptions{})
					must(t, err)
					if restart {
						// Restart with no local objects: only the pushed remote survives.
						must(t, os.RemoveAll(cfg.WorkDir))
						g, err = gitops.New(t.Context(), cfg)
						must(t, err)
						w.Git = g
					}
					land := queue.Land{BatchID: "b1", Ref: "openmerge/queue/main/b1", ExpectedBaseSHA: base, TestedSHA: tested, PRs: pins, Method: method}
					gate := (&Server{}).stageAGate(t.Context(), w, store.ClaimedAction{}, queue.Config{}, queue.State{})
					messages, err := gate.ReadTestedMessages(t.Context(), land)
					must(t, err)
					wantMessages := depth
					if method == queue.FastForward {
						wantMessages = 1
					}
					if len(messages) != wantMessages {
						t.Fatalf("messages=%d, want %d", len(messages), wantMessages)
					}
					for i, message := range messages {
						metadata, err := forge.ParseChangeTrailers(message)
						must(t, err)
						wantMembers := 1
						if method == queue.FastForward {
							wantMembers = depth
						}
						if len(metadata) != wantMembers || metadata[0].ChangeID != prs[i].Metadata.ChangeID {
							t.Fatalf("member %d: %+v", i, metadata)
						}
					}
					expression, err := condition.Parse("check-success = ci")
					must(t, err)
					runs := []forge.CheckRun{{Name: "ci", Source: "app:15368", ID: 701, Attempt: 2, State: forge.Success, HeadBranch: land.Ref}}
					values, err := checkSnapshot(expression, runs)
					must(t, err)
					builder, err := landingCheckEvidence(queue.State{ConfigCommit: "real-config", ConfigDigest: strings.Repeat("c", 64)}, strings.Repeat("d", 64), land, runs, time.Now(), "real-start")
					must(t, err)
					for _, pin := range pins {
						must(t, builder.RecordMember(pin.PR, pin.HeadSHA, expression, values))
					}
					recorded := builder.Event()
					_, encoded, err := queue.EncodeEvent(recorded)
					must(t, err)
					decoded, err := queue.DecodeEvent(encoded)
					must(t, err)
					if decoded.(queue.LandingChecksRecorded).TestedSHA != tested {
						t.Fatal("real tested object not bound")
					}
					if method != queue.FastForward {
						// An extra queue-generated segment must fail closed at the
						// batch-derived limit, even though all objects are readable.
						extra, err := g.BuildTestRef(t.Context(), w.repo(), forge.BuildRequest{Ref: "extra-composition", BaseSHA: tested, PRs: prs[:1], Method: forge.MergeMethod(method), BatchID: "extra-gate"}, forge.PushOptions{})
						must(t, err)
						overlong := land
						overlong.TestedSHA = extra
						if _, err := gate.ReadTestedMessages(t.Context(), overlong); err == nil || !strings.Contains(err.Error(), "tested member history exceeds composition limit") {
							t.Fatalf("overlong composition accepted: %v", err)
						}
					}
					fence, err := g.EnsureFence(t.Context(), w.repo(), "test-fence", forge.PushOptions{})
					must(t, err)
					head := ""
					if method == queue.FastForward {
						head = pins[len(pins)-1].HeadSHA
						must(t, g.VerifyFastForward(t.Context(), w.repo(), base, tested, head))
					}
					var settledEvidence func(string, string)
					if os.Getenv("OPENMERGE_TEST_DATABASE_URL") != "" {
						settledEvidence = recordRealGitCheckStart(t, key, land, recorded, fence)
					}
					nextFence, err := g.Land(t.Context(), w.repo(), forge.LandRequest{Base: "main", ExpectedBaseSHA: base, TestedSHA: tested, FastForwardHeadSHA: head, Fence: "test-fence", ExpectedFence: fence, NextFenceID: "land/" + recorded.AttemptID, PRs: prs}, forge.PushOptions{})
					must(t, err)
					got := git("", "--git-dir="+remote, "rev-parse", "refs/heads/main")
					if got != recorded.LandedSHA {
						t.Fatalf("landed %s, want %s", got, recorded.LandedSHA)
					}
					if settledEvidence != nil {
						settledEvidence(got, nextFence)
					}
				})
			}
		}
	}
}
