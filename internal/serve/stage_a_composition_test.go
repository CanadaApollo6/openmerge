package serve

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type compositionFixture struct {
	w      *worker
	g      *gitops.Repo
	base   string
	prs    []forge.BuildPR
	pins   []queue.BatchPR
	git    func(string, ...string) string
	remote string
}

func newCompositionFixture(t *testing.T, counts []int) compositionFixture {
	t.Helper()
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
	key := store.QueueKey{Installation: "7", Repository: "local/composition", BaseBranch: "main"}
	f := compositionFixture{base: base, git: git, remote: remote}
	// fast-import creates large source histories in one process, without a
	// checkout or one process per commit. Replaying large histories is !short.
	var input strings.Builder
	mark := 0
	for i, n := range counts {
		for j := 0; j < n; j++ {
			mark++
			msg := fmt.Sprintf("source %d/%d\n", i+1, j)
			fmt.Fprintf(&input, "commit refs/pull/%d/head\nmark :%d\ncommitter Fixture <fixture@example.test> 1700000000 +0000\ndata %d\n%s", i+1, mark, len(msg), msg)
			if j == 0 {
				fmt.Fprintf(&input, "from %s\n", base)
			} else {
				fmt.Fprintf(&input, "from :%d\n", mark-1)
			}
			input.WriteString("\n")
		}
	}
	git(input.String(), "--git-dir="+remote, "fast-import", "--quiet")
	for i := range counts {
		head := git("", "--git-dir="+remote, "rev-parse", fmt.Sprintf("refs/pull/%d/head", i+1))
		id, err := key.ChangeID(i + 1)
		must(t, err)
		f.prs = append(f.prs, forge.BuildPR{Number: i + 1, HeadSHA: head, Metadata: &forge.ChangeMetadata{ChangeID: string(id), Revision: 1}})
		f.pins = append(f.pins, queue.BatchPR{PR: i + 1, HeadSHA: head})
	}
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(dir, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
	must(t, err)
	f.g = g
	f.w = &worker{Target: Target{Key: key, Git: g}, stageA: &stageAPolicy{}}
	return f
}

type compositionHistorySpy struct {
	*gitops.Repo
	reads int
}

func (g *compositionHistorySpy) CommitsBetween(ctx context.Context, repo forge.RepoRef, base, head string, limit int) ([]string, bool, error) {
	g.reads++
	return g.Repo.CommitsBetween(ctx, repo, base, head, limit)
}

func TestStageARebaseCompositionBeforeCI(t *testing.T) {
	limit := forge.MaxFirstParentCommits
	if testing.Short() {
		limit = 4
	}
	for _, tc := range []struct {
		name         string
		counts       []int
		legacy       bool
		wantBatches  int
		wantRejected int
	}{
		{"oversized-member", []int{limit + 1}, false, 0, 1},
		{"oversized-beside-supported", []int{limit + 1, 1}, false, 1, 1},
		{"aggregate-splits", []int{limit - 1, 2}, false, 2, 0},
		{"at-limit", []int{limit}, false, 1, 0},
		{"legacy-over-limit", []int{limit + 1}, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompositionFixture(t, tc.counts)
			state := queue.State{BaseBranch: "main"}
			at := time.Now()
			events := []queue.Event{}
			emit := func(e queue.Event) error {
				_, data, err := queue.EncodeEvent(e)
				if err != nil {
					return err
				}
				decoded, err := queue.DecodeEvent(data)
				if err != nil {
					return err
				}
				events = append(events, decoded)
				state = queue.Apply(state, decoded)
				return nil
			}
			must(t, emit(queue.BaseMoved{SHA: f.base}))
			fence, err := f.g.EnsureFence(t.Context(), f.w.repo(), "test-fence")
			must(t, err)
			must(t, emit(queue.FenceInitialized{Token: fence}))
			for _, p := range f.pins {
				must(t, emit(queue.PRObserved{PR: p.PR, HeadSHA: p.HeadSHA, Eligible: true, Trusted: true}))
				must(t, emit(queue.EnqueueRequested{PR: p.PR, At: at}))
			}
			spy := &compositionHistorySpy{Repo: f.g}
			f.w.Git = spy
			x := executor.Executor{Git: spy, Repo: f.w.repo(), AllowNoRequiredChecks: true}
			reads := 0
			if !tc.legacy {
				x.RebaseComposition = func(ctx context.Context, a queue.BuildTestRef) (queue.RebaseCompositionObserved, error) {
					reads++
					return readRebaseComposition(ctx, f.w, a, limit)
				}
				x.BuildMetadata = func(_ queue.State, _ executor.BuildChangeKey, b queue.Batch, _ string) ([]*forge.ChangeMetadata, error) {
					result := []*forge.ChangeMetadata{}
					for _, p := range b.PRs {
						result = append(result, f.prs[p.PR-1].Metadata)
					}
					return result, nil
				}
			}
			cfg := queue.Config{MergeMethod: queue.Rebase, BatchMaxSize: len(f.pins)}
			batches, rejected := 0, 0
			for turn := 0; turn < 12; turn++ {
				var build *queue.BuildTestRef
				for _, a := range queue.Plan(cfg, state, at) {
					switch a := a.(type) {
					case queue.Dequeue:
						if a.Reason != queue.RebaseCompositionExceeded {
							t.Fatalf("unexpected dequeue: %+v", a)
						}
						detail := state.DequeueDetail(a.PR, a.Reason)
						if !strings.Contains(detail, "use squash or merge, or split the change") {
							t.Fatal(detail)
						}
						rejected++
						must(t, emit(queue.Dequeued{PR: a.PR, Reason: a.Reason, AdmissionID: a.AdmissionID}))
					case queue.BuildTestRef:
						b := a
						build = &b
					}
				}
				if build == nil {
					break
				}
				must(t, x.Execute(t.Context(), *build, emit))
				b := state.Active
				if b == nil {
					continue
				}
				if b.TestedSHA == "" {
					t.Fatal("batch did not build")
				}
				if !tc.legacy {
					_, err := testedMessages(t.Context(), f.w, queue.Land{ExpectedBaseSHA: b.BaseSHA, TestedSHA: b.TestedSHA, PRs: b.PRs, Method: queue.Rebase})
					must(t, err)
				}
				// This is the CI boundary: only a supported composition reaches it.
				must(t, emit(queue.ChecksCompleted{TestedSHA: b.TestedSHA, Success: true}))
				must(t, emit(queue.LandingStarted{BatchID: b.ID, AttemptID: fmt.Sprint(turn), FenceToken: fence, At: at}))
				next := fmt.Sprintf("land/%d", turn)
				result, err := f.g.Land(t.Context(), f.w.repo(), forge.LandRequest{Base: "main", ExpectedBaseSHA: b.BaseSHA, TestedSHA: b.TestedSHA, Fence: "test-fence", ExpectedFence: fence, NextFenceID: next})
				must(t, err)
				must(t, emit(queue.Landed{BatchID: b.ID, NewBaseSHA: b.TestedSHA, NewFence: result}))
				fence = result
				batches++
				if got := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/main"); got != state.BaseSHA {
					t.Fatalf("landed %s != %s", got, state.BaseSHA)
				}
			}
			if batches != tc.wantBatches || rejected != tc.wantRejected {
				t.Fatalf("batches=%d rejected=%d", batches, rejected)
			}
			if tc.legacy && (reads != 0 || spy.reads != 0) {
				t.Fatalf("legacy history reads: callback=%d reader=%d", reads, spy.reads)
			}
			// Reconstruct the durable projection from the complete encoded journal.
			replay := queue.State{BaseBranch: "main"}
			for _, e := range events {
				replay = queue.Apply(replay, e)
			}
			if len(replay.Entries) != 0 || replay.BaseSHA != state.BaseSHA {
				t.Fatalf("replay lost result: %+v", replay)
			}
		})
	}
}

func TestStageARealGitSpeculativeDepthFour(t *testing.T) {
	for _, method := range []queue.MergeMethod{queue.Merge, queue.Squash, queue.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newCompositionFixture(t, []int{2, 2, 2, 2})
			base := f.base
			lands := []queue.Land{}
			// Build all four batches before landing any; each pins its predecessor's
			// speculative tested SHA rather than the current remote main.
			for i := 0; i < 4; i++ {
				tested, err := f.g.BuildTestRef(t.Context(), f.w.repo(), forge.BuildRequest{Ref: fmt.Sprintf("speculative-%d", i), BaseSHA: base, PRs: f.prs[i : i+1], Method: forge.MergeMethod(method), BatchID: fmt.Sprint(i)})
				must(t, err)
				lands = append(lands, queue.Land{ExpectedBaseSHA: base, TestedSHA: tested, PRs: f.pins[i : i+1], Method: method})
				base = tested
			}
			fence, err := f.g.EnsureFence(t.Context(), f.w.repo(), "test-fence")
			must(t, err)
			for i, land := range lands {
				_, err := testedMessages(t.Context(), f.w, land)
				must(t, err)
				next := fmt.Sprintf("speculative-land/%d", i)
				result, err := f.g.Land(t.Context(), f.w.repo(), forge.LandRequest{Base: "main", ExpectedBaseSHA: land.ExpectedBaseSHA, TestedSHA: land.TestedSHA, Fence: "test-fence", ExpectedFence: fence, NextFenceID: next})
				must(t, err)
				fence = result
				if got := f.git("", "--git-dir="+f.remote, "rev-parse", "refs/heads/main"); got != land.TestedSHA {
					t.Fatalf("batch %d landed %s", i, got)
				}
			}
		})
	}
}
