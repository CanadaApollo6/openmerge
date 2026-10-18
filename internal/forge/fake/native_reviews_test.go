package fake

import (
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
)

func TestNativeReviewContract(t *testing.T) {
	forgetest.RunNativeReviewReader(t, func(_ *testing.T) forgetest.NativeReviewEnv {
		f := New()
		repo := forge.RepoRef{Path: "acme/project"}
		return forgetest.NativeReviewEnv{Reader: f, Identities: f, Repo: repo, Set: func(value forge.NativeReviews) { f.SetNativeReviews(repo, 1, value) }, SetGrant: func(user forge.UserIdentity) { f.SetIdentity(user); f.SetWriteGrant(repo, user.ID, "write") }, Fail: func(err error) { f.FailNext("NativeReviews", err) }}
	})
}

func TestNativeReviewIsolationAndFailures(t *testing.T) {
	f := New()
	repo := forge.RepoRef{Path: "acme/project"}
	for _, r := range []forge.RepoRef{repo, {Path: "other/repo"}} {
		if _, err := f.NativeReviews(t.Context(), r, 1); err == nil {
			t.Fatal("unconfigured data must be unknown")
		}
	}
	value := forge.NativeReviews{Reviews: []forge.NativeReview{{Dismissal: &forge.NativeDismissal{EventID: 1}}}}
	f.SetNativeReviews(repo, 1, value)
	value.Reviews[0].Dismissal.EventID = 2
	got, err := f.NativeReviews(t.Context(), repo, 1)
	if err != nil || got.Reviews[0].Dismissal.EventID != 1 {
		t.Fatal("setter did not isolate input")
	}
	if _, err := f.NativeReviews(t.Context(), repo, 2); err == nil {
		t.Fatal("evidence crossed PR boundary")
	}
}
