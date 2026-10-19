package gitops

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestAcquireCommitsRetainsExactObjectsAcrossMaintenance(t *testing.T) {
	for _, method := range []forge.MergeMethod{forge.Merge, forge.Squash, forge.Rebase, forge.FastForward} {
		t.Run(string(method), func(t *testing.T) {
			f := newFixture(t)
			f.g.cfg.GCThresholdBytes = 1
			tested, err := f.g.BuildTestRef(t.Context(), testRepo, forge.BuildRequest{Ref: "test-acquisition", BaseSHA: f.base, PRs: f.prs[:1], Method: method, BatchID: "acquire"})
			must(t, err)
			if _, err := f.g.ReadCommitMessage(t.Context(), testRepo, tested); err == nil {
				t.Fatal("forced GC retained unreferenced tested object")
			}
			// Move the ref. Acquisition must still obtain the recorded object ID.
			gitCommand(t, f.remote, "", nil, "update-ref", "refs/heads/test-acquisition", f.base)
			// Local transport serves exact unreachable IDs, as GitHub does for known
			// commits. Failure to serve an old ID in another transport must fail closed.
			release, err := f.g.AcquireCommits(t.Context(), testRepo, []string{f.base, tested})
			must(t, err)
			defer release()
			second, err := f.g.AcquireCommits(t.Context(), testRepo, []string{tested})
			must(t, err)
			second()
			second() // Releases are independent and idempotent.
			_, _, err = f.g.CommitsBetween(t.Context(), testRepo, f.base, f.prs[0].HeadSHA, 10)
			must(t, err) // Its post-operation GC must preserve the first lease.
			f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
				t.Fatal("cache-only reader resolved remote")
				return "", Auth{}, nil
			}
			if _, err := f.g.ReadCommitMessage(t.Context(), testRepo, tested); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.g.FirstParentCommits(t.Context(), testRepo, f.base, tested, 10); err != nil {
				t.Fatal(err)
			}
			release()
			dir := filepath.Join(f.g.cfg.WorkDir, fmt.Sprintf("%x.git", sha256.Sum256([]byte(testRepo.Path))))
			if refs := gitCommand(t, dir, "", nil, "for-each-ref", "--format=%(refname)", "refs/openmerge-cache/acquired/"); refs != "" {
				t.Fatalf("leaked leases: %s", refs)
			}
			gitCommand(t, dir, "", nil, "gc", "--prune=now", "--quiet")
			if _, err := f.g.ReadCommitMessage(t.Context(), testRepo, tested); err == nil {
				t.Fatal("released object remained readable after GC")
			}
		})
	}
}

func TestAcquireCommitsValidatesPinsAndPolicyObjects(t *testing.T) {
	f := newFixture(t)
	f.g.cfg.GCThresholdBytes = 1
	config := "version: 1\nqueues: []\n"
	pin := f.commit(f.base, "policy", map[string]string{".openmerge.yml": config, ".github/CODEOWNERS": "* @owner\n"})
	f.push("policy", pin, false)
	for _, shas := range [][]string{nil, {"main"}, {f.base, strings.Repeat("f", 40)}, {gitCommand(t, f.remote, "", nil, "rev-parse", f.base+"^{tree}")}} {
		if release, err := f.g.AcquireCommits(t.Context(), testRepo, shas); err == nil {
			release()
			t.Fatalf("accepted invalid acquisition %v", shas)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.g.AcquireCommits(ctx, testRepo, []string{pin}); err == nil {
		t.Fatal("ignored cancellation")
	}
	release, err := f.g.AcquireCommits(t.Context(), testRepo, []string{pin})
	must(t, err)
	defer release()
	f.g.cfg.Remote = func(context.Context, forge.RepoRef) (string, Auth, error) {
		t.Fatal("local policy read resolved remote")
		return "", Auth{}, nil
	}
	data, err := f.g.LocalConfig(t.Context(), testRepo, pin)
	must(t, err)
	if string(data) != config {
		t.Fatalf("config=%q", data)
	}
	governing, err := f.g.LocalGoverningFiles(t.Context(), testRepo, pin)
	must(t, err)
	if governing.CommitSHA != pin || governing.SelectedCodeowners != ".github/CODEOWNERS" {
		t.Fatalf("governing=%+v", governing)
	}
}
