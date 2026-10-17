package github

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestInstallationDiscoveryPaginatesAndRevalidates(t *testing.T) {
	f := newAPI(t)
	before := f.notModified
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if path != "/app/installations" && path != "/installation/repositories" {
			return false
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("unbounded page")
		}
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page != "2" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3%s?per_page=100&page=2>; rel="next"`, f.server.URL, path))
		}
		if path == "/app/installations" {
			if page == "2" {
				_, _ = fmt.Fprint(w, `[{"id":8,"suspended_at":"2026-10-16T00:00:00Z"}]`)
			} else {
				_, _ = fmt.Fprint(w, `[{"id":7}]`)
			}
		} else if page == "2" {
			_, _ = fmt.Fprint(w, `{"total_count":2,"repositories":[{"full_name":"Other/Repo"}]}`)
		} else {
			_, _ = fmt.Fprint(w, `{"total_count":2,"repositories":[{"full_name":"CanadaApollo6/openmerge"}]}`)
		}
		return true
	}
	for range 2 {
		f.mu.Lock()
		f.clock = f.clock.Add(10 * time.Minute)
		f.mu.Unlock()
		installations, err := f.app.ListInstallations(t.Context())
		must(t, err)
		if len(installations) != 2 || installations[0].ID != 7 || installations[0].Suspended || !installations[1].Suspended {
			t.Fatalf("installations=%+v", installations)
		}
		repos, err := f.driver.ListRepositories(t.Context())
		must(t, err)
		if len(repos) != 2 || repos[0] != "CanadaApollo6/openmerge" || repos[1] != "Other/Repo" {
			t.Fatalf("repositories=%v", repos)
		}
	}
	if f.notModified-before != 4 {
		t.Fatalf("conditional page revalidations=%d, want 4", f.notModified-before)
	}
}

func TestFailedDiscoveryPageDiscardsPartialListing(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		path := strings.TrimPrefix(r.URL.Path, "/api/v3")
		if path != "/app/installations" && path != "/installation/repositories" {
			return false
		}
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(500)
			return true
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3%s?per_page=100&page=2>; rel="next"`, f.server.URL, path))
		if path == "/app/installations" {
			_, _ = fmt.Fprint(w, `[{"id":7}]`)
		} else {
			_, _ = fmt.Fprint(w, `{"total_count":2,"repositories":[{"full_name":"owner/repo"}]}`)
		}
		return true
	}
	if list, err := f.app.ListInstallations(t.Context()); err == nil || list != nil {
		t.Fatalf("partial installations=%v err=%v", list, err)
	}
	if list, err := f.driver.ListRepositories(t.Context()); err == nil || list != nil {
		t.Fatalf("partial repositories=%v err=%v", list, err)
	}
}
