package gitops

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

var testRepo = forge.RepoRef{Path: "group/subgroup/repo"}
var epoch = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

const testFence = "openmerge/fence/main"

type fixture struct {
	t              *testing.T
	remote, source string
	base           string
	g              *Repo
	prs            []forge.BuildPR
	files          map[int]map[string]string
	heads          map[int]string
	pushes         map[int]int
	baseID         int
}

func gitCommand(t *testing.T, dir, input string, extraEnv []string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"--git-dir=" + dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(gitEnv(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost",
		"GIT_AUTHOR_DATE="+epoch.Format(time.RFC3339), "GIT_COMMITTER_DATE="+epoch.Format(time.RFC3339))
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, remote: filepath.Join(dir, "remote.git"), source: filepath.Join(dir, "source.git"),
		files: make(map[int]map[string]string), heads: make(map[int]string), pushes: make(map[int]int)}
	gitCommand(t, "", "", nil, "init", "--bare", "--template=", f.remote)
	gitCommand(t, "", "", nil, "init", "--bare", "--template=", f.source)
	f.base = f.commit("", "initial base", map[string]string{"base.txt": "base\n"})
	f.push("main", f.base, false)
	for _, n := range []int{1, 2, 3, 10, 11} {
		files := map[string]string{fmt.Sprintf("pr-%d.txt", n): fmt.Sprintf("PR %d\n", n)}
		if n >= 10 {
			files = map[string]string{"conflict.txt": fmt.Sprint(n)}
		}
		head := f.commit(f.base, fmt.Sprintf("PR %d", n), files)
		f.files[n], f.heads[n] = files, head
		gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(n))
		if n < 10 {
			f.prs = append(f.prs, forge.BuildPR{Number: n, HeadSHA: head})
		}
	}
	f.g = f.client(t, 0)
	return f
}

func headRef(pr int) string { return fmt.Sprintf("refs/pull/%d/head", pr) }

func (f *fixture) client(t *testing.T, clockOffset int) *Repo {
	t.Helper()
	r, err := New(context.Background(), Config{WorkDir: t.TempDir(),
		Remote:  func(context.Context, forge.RepoRef) (string, Auth, error) { return f.remote, Auth{}, nil },
		HeadRef: headRef, Now: func() time.Time { return epoch.Add(time.Duration(clockOffset) * time.Second) }})
	must(t, err)
	return r
}

func (f *fixture) commit(parent, message string, changes map[string]string) string {
	f.t.Helper()
	index := filepath.Join(f.t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	if parent == "" {
		gitCommand(f.t, f.source, "", env, "read-tree", "--empty")
	} else {
		gitCommand(f.t, f.source, "", env, "read-tree", parent)
	}
	for path, value := range changes {
		blob := gitCommand(f.t, f.source, value, nil, "hash-object", "-w", "--stdin")
		gitCommand(f.t, f.source, "", env, "update-index", "--add", "--cacheinfo", "100644", blob, path)
	}
	tree := gitCommand(f.t, f.source, "", env, "write-tree")
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return gitCommand(f.t, f.source, message+"\n", nil, args...)
}

func (f *fixture) fetch(sha string) {
	f.t.Helper()
	gitCommand(f.t, f.source, "", nil, "fetch", "--no-tags", f.remote, sha)
}

func (f *fixture) push(ref, sha string, force bool) {
	f.t.Helper()
	args := []string{"push"}
	if force {
		args = append(args, "--force")
	}
	gitCommand(f.t, f.source, "", nil, append(args, f.remote, sha+":refs/heads/"+ref)...)
}

func (f *fixture) read(ref string) string {
	f.t.Helper()
	sha, err := f.g.ReadRef(context.Background(), testRepo, ref)
	must(f.t, err)
	return sha
}

func (f *fixture) build(method forge.MergeMethod, prs []forge.BuildPR, batch string) string {
	f.t.Helper()
	sha, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/" + batch,
		BaseSHA: f.base, PRs: prs, Method: method, BatchID: batch})
	must(f.t, err)
	return sha
}

// Different caches allow competing operations to reach receive-pack at the
// same time; the per-cache mutex must not be what makes the contract pass.
type racingGit struct {
	*Repo
	other     *Repo
	bootstrap []*Repo
	next      atomic.Uint64
}

func (r *racingGit) AdvanceFence(ctx context.Context, repo forge.RepoRef, ref, expected, id string) (string, error) {
	return r.other.AdvanceFence(ctx, repo, ref, expected, id)
}

func (r *racingGit) EnsureFence(ctx context.Context, repo forge.RepoRef, ref string) (string, error) {
	return r.bootstrap[r.next.Add(1)%uint64(len(r.bootstrap))].EnsureFence(ctx, repo, ref)
}

func newGitEnv(t *testing.T) forgetest.Env {
	t.Helper()
	f := newFixture(t)
	race := &racingGit{Repo: f.g, other: f.client(t, 1)}
	race.bootstrap = []*Repo{f.g, race.other, f.client(t, 2), f.client(t, 3)}
	e := forgetest.Env{Git: race, Repo: testRepo, Base: "main", BaseSHA: f.base, PRs: slices.Clone(f.prs), CanRollbackBase: true}
	e.NewBase = func(t *testing.T) string {
		t.Helper()
		f.baseID++
		ref := fmt.Sprintf("contract-%d", f.baseID)
		f.push(ref, f.base, false)
		return ref
	}
	e.PushBase = func(ref, sha string) error { f.fetch(sha); f.push(ref, sha, false); return nil }
	e.RollbackBase = func(ref, sha string) error { f.push(ref, sha, true); return nil }
	e.PushPR = func(pr int) string {
		f.pushes[pr]++
		path := fmt.Sprintf("pr-%d-commit-%d.txt", pr, f.pushes[pr])
		changes := map[string]string{path: "new PR content\n"}
		head := f.commit(f.heads[pr], "contract push", changes)
		f.heads[pr], f.files[pr][path] = head, changes[path]
		gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(pr))
		return head
	}
	e.CommitCount = func(base, head string) int {
		f.fetch(head)
		n, err := strconv.Atoi(gitCommand(t, f.source, "", nil, "rev-list", "--count", base+".."+head))
		must(t, err)
		return n
	}
	for _, n := range []int{10, 11} {
		e.ConflictingPRs = append(e.ConflictingPRs, forge.BuildPR{Number: n, HeadSHA: f.heads[n]})
	}
	e.ContainsPR = func(sha string, pr int) bool {
		f.fetch(sha)
		for path, want := range f.files[pr] {
			if got := gitCommand(t, f.source, "", nil, "show", sha+":"+path); got != strings.TrimSpace(want) {
				return false
			}
		}
		return true
	}
	return e
}

func TestGitContract(t *testing.T) { forgetest.RunGit(t, newGitEnv) }

func TestCommitShapesAndMetadata(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			head := f.commit(f.heads[1], "second PR commit", map[string]string{"extra.txt": "second commit"})
			gitCommand(t, f.source, "", nil, "push", f.remote, head+":"+headRef(1))
			f.prs[0].HeadSHA = head
			f.g.cfg.Author = Identity{Name: "Author", Email: "author@example.test"}
			f.g.cfg.Committer = Identity{Name: "Committer", Email: "committer@example.test"}
			tested := f.build(method, f.prs[:2], "shape")
			f.fetch(tested)
			history := strings.Fields(gitCommand(t, f.source, "", nil, "rev-list", "--first-parent", "--reverse", f.base+".."+tested))
			want := 2
			if method == forge.Rebase {
				want = 3
			}
			if len(history) != want {
				t.Fatalf("%s produced %d commits, want %d", method, len(history), want)
			}
			parent := f.base
			for i, sha := range history {
				parents := strings.Fields(gitCommand(t, f.source, "", nil, "show", "-s", "--format=%P", sha))
				wantParents := []string{parent}
				if method == forge.Merge {
					wantParents = append(wantParents, f.prs[i].HeadSHA)
				}
				if !slices.Equal(parents, wantParents) {
					t.Fatalf("parents = %v, want %v", parents, wantParents)
				}
				parent = sha
			}
			metadata := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%an <%ae>|%cn <%ce>|%aI|%cI", tested)
			wantMetadata := "Author <author@example.test>|Committer <committer@example.test>|" + epoch.Format(time.RFC3339) + "|" + epoch.Format(time.RFC3339)
			if metadata != wantMetadata {
				t.Fatalf("metadata = %q, want %q", metadata, wantMetadata)
			}
			message := gitCommand(t, f.source, "", nil, "show", "-s", "--format=%B", tested)
			if !strings.HasSuffix(message, "\n\nOpenMerge-Batch: shape") {
				t.Fatalf("missing batch trailer: %q", message)
			}
			for _, path := range []string{"base.txt", "pr-1.txt", "pr-2.txt", "extra.txt"} {
				gitCommand(t, f.source, "", nil, "cat-file", "-e", tested+":"+path)
			}
			if again := f.build(method, f.prs[:2], "shape"); again != tested {
				t.Fatal("fixed clock and identical request changed the tested commit")
			}
		})
	}
}

func TestRebaseExplicitBaseCompatibility(t *testing.T) {
	f := newFixture(t)
	withFlag := f.build(forge.Rebase, f.prs, "compat")
	f.g.explicitBase = false
	withoutFlag := f.build(forge.Rebase, f.prs, "compat")
	if withFlag != withoutFlag {
		t.Fatal("synthetic merge base changed the replayed tree or commit metadata")
	}
}

func TestRebaseRewritesEvenMatchingIdentityAndClock(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.Author = Identity{Name: "Fixture", Email: "fixture@localhost"}
	f.g.cfg.Committer = f.g.cfg.Author
	tested := f.build(forge.Rebase, f.prs[:2], "matching-metadata")
	for _, pr := range f.prs[:2] {
		ancestor, err := f.g.IsAncestor(context.Background(), testRepo, pr.HeadSHA, tested)
		must(t, err)
		if ancestor {
			t.Fatal("replay retained the original commit when all metadata matched")
		}
	}
}

func TestRebaseRefusesIntermediateConflict(t *testing.T) {
	f := newFixture(t)
	// The final head agrees with PR 10, but PR 11's first commit conflicts.
	// Rebase must check each replay rather than just merging the final tree.
	fixed := f.commit(f.heads[11], "resolve later", map[string]string{"conflict.txt": "10"})
	gitCommand(t, f.source, "", nil, "push", f.remote, fixed+":"+headRef(11))
	prs := []forge.BuildPR{{Number: 10, HeadSHA: f.heads[10]}, {Number: 11, HeadSHA: fixed}}
	f.build(forge.Squash, prs, "final-head")
	_, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{Ref: "openmerge/queue/main/intermediate",
		BaseSHA: f.base, PRs: prs, Method: forge.Rebase, BatchID: "intermediate"})
	if err != forge.ErrConflict {
		t.Fatalf("intermediate replay conflict = %v", err)
	}
	if _, err := f.g.ReadRef(context.Background(), testRepo, "openmerge/queue/main/intermediate"); err == nil {
		t.Fatal("failed replay created its test ref")
	}
}

func TestConflictsPreserveRefsForEveryMethod(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			before := f.build(method, f.prs[:1], "conflict")
			_, err := f.g.BuildTestRef(context.Background(), testRepo, forge.BuildRequest{
				Ref: "openmerge/queue/main/conflict", BaseSHA: f.base, Method: method, BatchID: "conflict",
				PRs: []forge.BuildPR{{Number: 10, HeadSHA: f.heads[10]}, {Number: 11, HeadSHA: f.heads[11]}},
			})
			if err != forge.ErrConflict || f.read("main") != f.base || f.read("openmerge/queue/main/conflict") != before {
				t.Fatalf("conflict changed a ref or returned the wrong error: %v", err)
			}
		})
	}
}
