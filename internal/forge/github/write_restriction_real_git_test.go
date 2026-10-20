package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
)

// Use the CLI-backed contract/worker fixture pattern: a fresh bare remote and
// cache, real nested bases and both protected ref families. Only HTTP is fake;
// no production reader is selected and no actual GitHub enforcement is claimed.
func TestAuthorityReaderWithRealGitRefInventory(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	git := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_AUTHOR_DATE=2026-10-20T00:00:00Z", "GIT_COMMITTER_DATE=2026-10-20T00:00:00Z")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--bare", "--template=", remote)
	tree := git("", "--git-dir="+remote, "mktree")
	sha := git("fixture\n", "--git-dir="+remote, "commit-tree", tree)
	for _, ref := range []string{"main", "release/2026/oct", "openmerge/fence/main", "openmerge/fence/release/2026/oct", "openmerge/queue/nested/batch-1"} {
		git("", "--git-dir="+remote, "update-ref", "refs/heads/"+ref, sha)
	}
	inventory := func() string {
		return git("", "--git-dir="+remote, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	}
	before := inventory()
	g, err := gitops.New(t.Context(), gitops.Config{WorkDir: filepath.Join(dir, "cache"), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: func(n int) string { return fmt.Sprintf("refs/pull/%d/head", n) }})
	must(t, err)
	in := forgetest.WriteRestrictionInput()
	in.ExpectedRefs = []string{}
	for line := range strings.SplitSeq(before, "\n") {
		ref, head, _ := strings.Cut(line, " ")
		_, err := forge.BranchRef(ref)
		must(t, err)
		actual, err := g.ReadRef(t.Context(), in.Repo, ref)
		must(t, err)
		if actual != head {
			t.Fatalf("cache lost ref %s", ref)
		}
		in.ExpectedRefs = append(in.ExpectedRefs, ref)
	}
	f, e := authorityHTTPEnv(t)
	e.Configure("visible")
	e.SetTeam(forge.TeamIdentity{ID: 44, OrganizationID: 55, Organization: "acme", Slug: "platform"})
	observation, err := f.driver.ObserveWriteRestriction(t.Context(), in)
	must(t, err)
	if len(in.ExpectedRefs) != 5 || len(observation.Coverage) != 0 || len(observation.Findings) != 0 {
		t.Fatalf("reader interpreted actual refs prematurely: %+v", observation)
	}
	if after := inventory(); after != before {
		t.Fatalf("authority read mutated refs: before=%s after=%s", before, after)
	}
}
