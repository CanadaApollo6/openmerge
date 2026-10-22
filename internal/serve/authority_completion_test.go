package serve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAuthorityCompletionDiscovery(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, f, _ := fixture(t, repositoryConfig)
		request := store.AuthorityCompletionRequest{Installation: w.Key.Installation, Repository: w.Key.Repository, Digest: strings.Repeat("a", 64)}
		calls := 0
		failed := false
		changing := false
		// afterSnapshot runs between the durable snapshot and the rechecks.
		var afterSnapshot func()
		repo := Repository{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(string) Target { return w.Target }, Branches: func(context.Context) ([]string, error) {
			calls++
			if failed {
				return nil, errors.New("SECRET")
			}
			if calls%2 == 0 && afterSnapshot != nil {
				afterSnapshot()
				afterSnapshot = nil
			}
			if changing && calls%2 == 0 {
				return []string{"main", "release/v1"}, nil
			}
			return []string{"main"}, nil
		}}
		s.discovery = &Discovery{Installations: func(context.Context) ([]Installation, error) {
			return []Installation{{ID: w.Key.Installation, Repositories: func(context.Context) ([]Repository, error) { return []Repository{repo}, nil }}}, nil
		}}
		result, err := s.AuthorityCompletion(t.Context(), request)
		if err != nil || result.Complete || calls != 2 {
			t.Fatalf("discovery result %+v %v calls=%d", result, err, calls)
		}
		failed = true
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
		failed = false
		calls = 0
		changing = true
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
			t.Fatal("changed branch inventory accepted")
		}
		changing = false
		if _, err := s.AuthorityCompletion(t.Context(), request); err != nil {
			t.Fatal("mutable failure cached", err)
		}
		// The governing tip or default-branch NAME moving during the read is a
		// retryable failure, never a stale success; it is not cached either.
		ref := forge.RepoRef{Path: w.Key.Repository}
		tip, err := f.ReadRef(t.Context(), ref, "main")
		if err != nil {
			t.Fatal(err)
		}
		calls = 0
		afterSnapshot = func() {
			if _, err := f.PushBase(ref, "main", "outside advance", map[string]string{"x": "y"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil || !strings.Contains(err.Error(), "governing ref changed") {
			t.Fatalf("tip advance accepted: %v", err)
		}
		must(t, f.SetRef(ref, "release/same", tip))
		calls = 0
		afterSnapshot = func() {
			// Same commit, different default-branch name.
			head, err := f.ReadRef(t.Context(), ref, "main")
			must(t, err)
			must(t, f.SetRef(ref, "release/same", head))
			f.SetDefaultBranch(ref, "release/same")
		}
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil || !strings.Contains(err.Error(), "governing ref changed") {
			t.Fatalf("default-name change accepted: %v", err)
		}
		f.SetDefaultBranch(ref, "main")
		if _, err := s.AuthorityCompletion(t.Context(), request); err != nil {
			t.Fatal("ref change cached", err)
		}
		s.discovery.Installations = func(context.Context) ([]Installation, error) {
			return []Installation{{ID: w.Key.Installation, Repositories: func(context.Context) ([]Repository, error) { return []Repository{repo}, nil }}, {ID: "8", Repositories: func(context.Context) ([]Repository, error) { return []Repository{repo}, nil }}}, nil
		}
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
			t.Fatal("multiple installation scopes partially certified")
		}
		// Installation 8 has no registered partition. Its live scope must still
		// refuse completion when the worker allowlist admits only this worker.
		s.discovery.InstallationAllowlist = w.Key.Installation
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil || !strings.Contains(err.Error(), "across installations") {
			t.Fatalf("worker allowlist hid competing live installation: %v", err)
		}
		s.discovery.Installations = func(context.Context) ([]Installation, error) {
			return []Installation{{ID: w.Key.Installation, Repositories: func(context.Context) ([]Repository, error) { return []Repository{repo}, nil }}}, nil
		}
		s.discovery.RepositoryAllowlist = []string{"another/repo"}
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
			t.Fatal("excluded repository accepted")
		}
	})
}
