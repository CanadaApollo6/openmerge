package gitops

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestSharedRefValidationMatchesGit(t *testing.T) {
	s := &session{r: newFixture(t).g}
	refs := []string{"", "@", "main", "refs/heads/main", "refs/heads/@", "refs/tags/v1", "refs/heads/ünicode", "refs/heads/-option", "refs/heads/end.", "refs/heads/component./child", "refs/heads/a.lock/child", "refs/heads/a.lock.lock", "refs/heads/a@{b", "refs/heads/a..b", "refs/heads/.a", "refs//heads/a", "/refs/heads/a", "refs/heads/a/"}
	for c := 1; c < 256; c++ {
		refs = append(refs, "refs/heads/a"+string([]byte{byte(c)})+"b")
	}
	for i, ref := range refs {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, _, err := s.run(context.Background(), "", "check-ref-format", ref)
			if got := forge.ValidateRefName(ref); (got == nil) != (err == nil) {
				t.Fatalf("ref %q: shared=%v git=%v", ref, got, err)
			}
		})
	}
	if err := forge.ValidateRefName("refs/heads/" + strings.Repeat("x", 1<<10) + "\x00"); err == nil {
		t.Fatal("NUL ref accepted")
	}
}
