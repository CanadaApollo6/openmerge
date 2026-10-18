package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type diffHistoryFixture struct {
	*fixture
	req          forge.RevisionDiffRequest
	c, s, k, dir string
}

// Astra's round-2 reproduction: C -> S -> K, C -> D, merge(K,D) -> B,
// S -> H. Cutting K's parents changes the apparent base from S to C and
// conceals H's code.go revert behind a docs-only inventory.
func newDiffHistoryFixture(t *testing.T) diffHistoryFixture {
	t.Helper()
	f := newFixture(t)
	c := f.diffCommit(f.base, "C", map[string]diffEntry{"code.go": {mode: "100644", content: "original\n"}})
	s := f.diffCommit(c, "S", map[string]diffEntry{"code.go": {mode: "100644", content: "changed\n"}})
	k := f.diffCommit(s, "K", nil)
	d := f.diffCommit(c, "D", nil)
	b := f.diffCommit(k, "B", nil, d)
	h := f.diffCommit(s, "H", map[string]diffEntry{"code.go": {mode: "100644", content: "original\n"}, "docs.md": {mode: "100644", content: "docs\n"}})
	req := forge.RevisionDiffRequest{BaseSHA: b, HeadSHA: h}
	got, err := f.g.RevisionDiff(t.Context(), testRepo, req)
	assertDiffHistoryComplete(t, got, err, s)
	sess, unlock, err := f.g.open(t.Context(), testRepo)
	must(t, err)
	dir := sess.dir
	unlock()
	must(t, os.MkdirAll(filepath.Join(dir, "info"), 0700))
	f.g.cfg.GCInterval, f.g.cfg.GCThresholdBytes = -1, -1
	return diffHistoryFixture{fixture: f, req: req, c: c, s: s, k: k, dir: dir}
}

func assertDiffHistoryComplete(t *testing.T, got forge.RevisionDiff, err error, base string) {
	t.Helper()
	paths := []string{}
	for _, file := range got.Files {
		paths = append(paths, file.NewPath)
	}
	if err != nil || got.Inventory.Completeness != forge.DiffComplete || len(got.Inventory.Reasons) != 0 || got.MergeBaseSHA != base || !reflect.DeepEqual(paths, []string{"code.go", "docs.md"}) {
		t.Fatalf("history must include the code revert: %+v err=%v", got, err)
	}
}

func assertDiffHistoryIncomplete(t *testing.T, got forge.RevisionDiff, err error, reason forge.DiffFailureReason) {
	t.Helper()
	if got.Inventory.Completeness != forge.DiffIncomplete || !reflect.DeepEqual(got.Inventory.Reasons, []forge.DiffFailureReason{reason}) || (err != nil) != (reason == forge.DiffReadFailure) {
		t.Fatalf("want incomplete/%s: %+v err=%v", reason, got, err)
	}
}

func TestRevisionDiffAlteredHistory(t *testing.T) {
	for _, mode := range []string{"replace", "packed-replace", "graft", "empty-graft"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffHistoryFixture(t)
			switch mode {
			case "replace", "packed-replace":
				gitCommand(t, f.dir, "", nil, "replace", "--graft", f.k)
				if mode == "packed-replace" {
					gitCommand(t, f.dir, "", nil, "pack-refs", "--all")
				}
			case "graft":
				must(t, os.WriteFile(filepath.Join(f.dir, "info", "grafts"), []byte(f.k+"\n"), 0600))
			case "empty-graft":
				must(t, os.WriteFile(filepath.Join(f.dir, "info", "grafts"), nil, 0600))
			}
			if mode != "empty-graft" && !strings.HasSuffix(gitCommand(t, f.dir, "", nil, "merge-base", f.req.BaseSHA, f.req.HeadSHA), f.c) {
				t.Fatal("fixture did not reproduce altered ancestry")
			}
			// Suppressing the mutation yields the correct inventory, but its presence
			// must still be reported as altered_history rather than trusted evidence.
			got, err := f.g.RevisionDiff(t.Context(), testRepo, f.req)
			assertDiffHistoryIncomplete(t, got, err, forge.DiffAlteredHistory)
			if got.MergeBaseSHA != "" || len(got.Files) != 0 {
				t.Fatalf("accepted altered history: %+v", got)
			}
		})
	}
}

func TestRevisionDiffShallowHistoryChangesDuringRead(t *testing.T) {
	for _, operation := range []string{"shallow-status", "merge-base", "rev-list", "diff-tree", "blob"} {
		for _, when := range []string{"before", "after"} {
			t.Run(operation+"/"+when, func(t *testing.T) {
				f := newDiffHistoryFixture(t)
				wrapper := filepath.Join(t.TempDir(), "git")
				target := operation
				mutation := "printf '%s\\n' " + shellQuote(f.k) + " > " + shellQuote(filepath.Join(f.dir, "shallow")) + "\n"
				execute := shellQuote(f.g.git) + " \"$@\"\nstatus=$?\n"
				if operation == "shallow-status" {
					target = "--is-shallow-repository"
					// A stale status must not override the file observation.
					execute = "printf 'false\\n'\nstatus=0\n"
				}
				body := mutation + execute
				if when == "after" {
					body = execute + mutation
				}
				writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = "+shellQuote(target)+" ]; then\n"+body+"exit \"$status\"\nfi\ndone\nexec "+shellQuote(f.g.git)+" \"$@\"\n")
				f.g.git = wrapper
				got, err := f.g.RevisionDiff(t.Context(), testRepo, f.req)
				assertDiffHistoryIncomplete(t, got, err, forge.DiffShallowHistory)
			})
		}
	}
}

func TestRevisionDiffIgnoresTransientHistoryAlterations(t *testing.T) {
	for _, mode := range []string{"shallow", "replace", "graft"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffHistoryFixture(t)
			realGit := shellQuote(f.g.git)
			path := filepath.Join(f.dir, "shallow")
			mutation := "printf '%s\\n' " + shellQuote(f.k) + " > " + shellQuote(path) + "\n"
			cleanup := "rm " + shellQuote(path) + "\n"
			switch mode {
			case "replace":
				mutation = "env -u GIT_NO_REPLACE_OBJECTS " + realGit + " --git-dir=" + shellQuote(f.dir) + " replace --graft " + f.k + " || exit 90\n"
				cleanup = "env -u GIT_NO_REPLACE_OBJECTS " + realGit + " --git-dir=" + shellQuote(f.dir) + " replace -d " + f.k + " >/dev/null || exit 91\n"
			case "graft":
				path = filepath.Join(f.dir, "info", "grafts")
				mutation = "printf '%s\\n' " + shellQuote(f.k) + " > " + shellQuote(path) + "\n"
				cleanup = "rm " + shellQuote(path) + "\n"
			}
			wrapper := filepath.Join(t.TempDir(), "git")
			// The file/ref exists only inside the merge-base process window, beyond
			// what surrounding filesystem checks can see. Git must ignore it anyway.
			writeExecutable(t, wrapper, "for arg do\nif [ \"$arg\" = merge-base ]; then\n"+mutation+realGit+" \"$@\"\nstatus=$?\n"+cleanup+"exit \"$status\"\nfi\ndone\nexec "+realGit+" \"$@\"\n")
			f.g.git = wrapper
			got, err := f.g.RevisionDiff(t.Context(), testRepo, f.req)
			assertDiffHistoryComplete(t, got, err, f.s)
		})
	}
}

func TestRevisionDiffMissingPromisorAncestry(t *testing.T) {
	f := newDiffHistoryFixture(t)
	gitCommand(t, f.remote, "", nil, "config", "uploadpack.allowFilter", "true")
	gitCommand(t, f.remote, "", nil, "commit-graph", "write", "--reachable")
	graph, err := os.ReadFile(filepath.Join(f.remote, "objects", "info", "commit-graph"))
	must(t, err)
	ids := strings.Fields(gitCommand(t, f.remote, "", nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)"))
	for _, missing := range []string{f.k, f.c} {
		name := "intermediate-K"
		if missing == f.c {
			name = "older-than-base-C"
		}
		for _, withGraph := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/commit-graph=%t", name, withGraph), func(t *testing.T) {
				r := f.client(t, 0)
				r.cfg.GCInterval, r.cfg.GCThresholdBytes = -1, -1
				sess, unlock, err := r.open(t.Context(), testRepo)
				must(t, err)
				dir := sess.dir
				unlock()
				must(t, os.RemoveAll(dir))
				gitCommand(t, "", "", nil, "clone", "--bare", "--filter=blob:none", "file://"+f.remote, dir)
				// Hydrate the fixture explicitly before testing read-only behavior.
				gitCommand(t, dir, strings.Join(ids, "\n")+"\n", nil, "cat-file", "--batch")
				control, err := r.RevisionDiff(t.Context(), testRepo, f.req)
				assertDiffHistoryComplete(t, control, err, f.s)
				// Reproduce the reviewer's genuine blob:none promisor store, retaining
				// all inspected blobs but omitting exactly one commit from its new pack.
				included := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == missing })
				must(t, os.RemoveAll(filepath.Join(dir, "objects")))
				must(t, os.MkdirAll(filepath.Join(dir, "objects", "pack"), 0700))
				must(t, os.MkdirAll(filepath.Join(dir, "objects", "info"), 0700))
				prefix := filepath.Join(dir, "objects", "pack", "pack")
				pack := gitCommand(t, f.remote, strings.Join(included, "\n")+"\n", nil, "pack-objects", prefix)
				must(t, os.WriteFile(prefix+"-"+pack+".promisor", nil, 0600))
				if withGraph {
					must(t, os.WriteFile(filepath.Join(dir, "objects", "info", "commit-graph"), graph, 0600))
				}
				if missing == f.k && !withGraph {
					// Model older supported Git, which ignores GIT_NO_LAZY_FETCH.
					wrapper := filepath.Join(t.TempDir(), "git")
					writeExecutable(t, wrapper, "unset GIT_NO_LAZY_FETCH\nexec "+shellQuote(r.git)+" \"$@\"\n")
					r.git = wrapper
				}
				// Keep the promisor remote available: a lazy fetch would repair the
				// fixture and falsely permit complete evidence instead of failing closed.
				got, err := r.RevisionDiff(t.Context(), testRepo, f.req)
				assertDiffHistoryIncomplete(t, got, err, forge.DiffReadFailure)
				if got.MergeBaseSHA != "" || len(got.Files) != 0 {
					t.Fatalf("accepted missing ancestry: %+v", got)
				}
				// Disabling lazy fetch at the independent probe proves the reader did not
				// hydrate the missing commit as a side effect of checking completeness.
				probe := &session{r: r, dir: dir, diffRead: true}
				out, _, err := probe.run(t.Context(), "", "cat-file", "-e", missing)
				if err == nil {
					t.Fatalf("reader fetched missing commit %s: %s", missing, out)
				}
			})
		}
	}
}

func TestRevisionDiffShallowSinceAndDeepen(t *testing.T) {
	f := newDiffHistoryFixture(t)
	prev := f.c
	for i := 1; i <= 5; i++ {
		tree := gitCommand(t, f.source, "", nil, "rev-parse", f.s+"^{tree}")
		date := epoch.Add(time.Duration(i) * 24 * time.Hour).Format(time.RFC3339)
		prev = gitCommand(t, f.source, fmt.Sprintf("dated-%d", i), []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit-tree", tree, "-p", prev)
	}
	f.push("dated", prev, false)
	r := f.client(t, 0)
	sess, unlock, err := r.open(t.Context(), testRepo)
	must(t, err)
	dir := sess.dir
	unlock()
	since := epoch.Add(3 * 24 * time.Hour).Format(time.RFC3339)
	gitCommand(t, dir, "", nil, "fetch", "--no-write-fetch-head", "--shallow-since="+since, "file://"+f.remote, prev)
	req := forge.RevisionDiffRequest{BaseSHA: prev, HeadSHA: prev}
	for _, mode := range []string{"shallow-since", "deepen-still-shallow"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "deepen-still-shallow" {
				gitCommand(t, dir, "", nil, "fetch", "--no-write-fetch-head", "--deepen=1", "file://"+f.remote, prev)
			}
			got, err := r.RevisionDiff(t.Context(), testRepo, req)
			assertDiffHistoryIncomplete(t, got, err, forge.DiffShallowHistory)
		})
	}
}
