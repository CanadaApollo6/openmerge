package serve

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestAuthorityCompletionDiscovery(t *testing.T) {
	testSynctest(t, func(t *testing.T) {
		s, w, _, _ := fixture(t, repositoryConfig)
		request := store.AuthorityCompletionRequest{Installation: w.Key.Installation, Repository: w.Key.Repository, Digest: strings.Repeat("a", 64)}
		calls := 0
		failed := false
		changing := false
		repo := Repository{Installation: w.Key.Installation, Name: w.Key.Repository, Target: func(string) Target { return w.Target }, Branches: func(context.Context) ([]string, error) {
			calls++
			if failed {
				return nil, errors.New("SECRET")
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
		s.discovery.RepositoryAllowlist = []string{"another/repo"}
		if _, err := s.AuthorityCompletion(t.Context(), request); err == nil {
			t.Fatal("excluded repository accepted")
		}
	})
}
