package fake

import (
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	"reflect"
	"testing"
)

func TestGoverningFilesContract(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	base, _ := f.Init(repo, "main", "fixture-fence")
	sha, err := f.PushBase(repo, "main", "governing", map[string]string{".openmerge.yml": "version: 1\n", "CODEOWNERS": "* @root\n", "docs/CODEOWNERS": "* @ignored\n", ".gitattributes": "* text\n", "src/.gitattributes": "*.go linguist-generated\n"})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunGoverningFiles(t, f, repo, sha)
	req := forge.GoverningFilesRequest{CommitSHA: sha, PathsComplete: true}
	before, err := f.GoverningFiles(t.Context(), repo, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.PushBase(repo, "main", "moves", map[string]string{"CODEOWNERS": "* @new\n", ".openmerge.yml": base}); err != nil {
		t.Fatal(err)
	}
	after, err := f.GoverningFiles(t.Context(), repo, req)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("branch movement changed governing evidence", err)
	}
}

func TestGoverningLimitationsContract(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/repo"}
	_, _ = f.Init(repo, "main", "fence")
	sha, err := f.PushBase(repo, "main", "limitations", map[string]string{".openmerge.yml": "\xff", ".github/CODEOWNERS/child": "* @nested", "CODEOWNERS": "* @fallback", ".gitattributes": "* text\n"})
	if err != nil {
		t.Fatal(err)
	}
	forgetest.RunGoverningLimitations(t, f, repo, sha)
}
