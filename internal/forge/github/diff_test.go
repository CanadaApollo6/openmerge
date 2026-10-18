package github

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestRevisionDiffHTTPGitAndInstallationCache(t *testing.T) {
	api := newAPI(t)
	root := t.TempDir()
	remote := filepath.Join(root, "acme", "project.git")
	must(t, os.MkdirAll(filepath.Dir(remote), 0700))
	localGit(t, "", "", "init", "--bare", "--template=", remote)
	blob := localGit(t, remote, "head\n", "hash-object", "-w", "--stdin")
	emptyTree := localGit(t, remote, "", "mktree")
	tree := localGit(t, remote, "100644 blob "+blob+"\thead.md\n", "mktree")
	base := localGit(t, remote, "base", "commit-tree", emptyTree)
	head := localGit(t, remote, "head", "commit-tree", tree, "-p", base)
	tip := localGit(t, remote, "base moves", "commit-tree", emptyTree, "-p", base)
	localGit(t, remote, "", "update-ref", "refs/heads/main", tip)
	localGit(t, remote, "", "update-ref", "refs/heads/feature", head)
	gitPath, err := exec.LookPath("git")
	must(t, err)
	var requests atomic.Int64
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || password != "secret-installation-1" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		requests.Add(1)
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	api.app.git, err = url.Parse(server.URL + "/")
	must(t, err)
	cache := t.TempDir()
	before := api.tokens
	reader, err := api.driver.RevisionDiffReader(t.Context(), cache)
	must(t, err)
	if api.tokens != before {
		t.Fatal("constructing capability made a remote request")
	}
	forgetest.RunRevisionDiff(t, reader, testRepo, forge.RevisionDiffRequest{BaseSHA: tip, HeadSHA: head}, base, "head.md")
	if requests.Load() == 0 {
		t.Fatal("git transport was not used")
	}
	repoName := fmt.Sprintf("%x.git", sha256.Sum256([]byte(testRepo.Path)))
	path := filepath.Join(cache, "7", repoName)
	if _, err = os.Stat(filepath.Join(path, "HEAD")); err != nil {
		t.Fatalf("installation cache missing: %v", err)
	}
	refs := localGit(t, path, "", "for-each-ref", "--format=%(refname)")
	if refs != "" {
		t.Fatalf("read created cache refs: %q", refs)
	}
	// Another installation gets a separate cache, even for the same repository.
	other := &Driver{app: api.app, installation: 8}
	_, err = other.RevisionDiffReader(t.Context(), cache)
	must(t, err)
	if _, err = os.Stat(filepath.Join(cache, strconv.FormatInt(other.installation, 10), "askpass")); err != nil {
		t.Fatal(err)
	}
	if _, err = api.driver.RevisionDiffReader(t.Context(), ""); err == nil {
		t.Fatal("empty cache accepted")
	}
	// A malformed repository fails before token acquisition or git transport.
	invalid, err := reader.RevisionDiff(t.Context(), forge.RepoRef{Path: "../bad"}, forge.RevisionDiffRequest{BaseSHA: tip, HeadSHA: head})
	if err == nil || invalid.Inventory.Completeness != forge.DiffIncomplete {
		t.Fatalf("%+v %v", invalid, err)
	}
}
