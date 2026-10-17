package github

import (
	"net/http"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestDefaultBranchMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, branch string
		status       int
		wantError    bool
	}{
		{"main", "main", 200, false},
		{"release default", "release/v1", 200, false},
		{"missing default", "", 200, true},
		{"unreadable", "", 403, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			reads := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/v3/repos/acme/project" {
					return false
				}
				reads++
				if tc.status != http.StatusOK {
					f.apiError(w, tc.status, "Forbidden")
				} else {
					f.json(w, map[string]any{"default_branch": tc.branch})
				}
				return true
			}
			branch, err := f.driver.DefaultBranch(t.Context(), forge.RepoRef{Path: "acme/project"})
			if (err != nil) != tc.wantError || branch != tc.branch || reads != 1 {
				t.Fatalf("DefaultBranch = %q, %v; reads=%d", branch, err, reads)
			}
		})
	}
}

func TestBranchListingPaginatesAndRevalidatesEveryPage(t *testing.T) {
	f := newAPI(t)
	reads := make(map[string]int)
	failSecond := false
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v3/repos/acme/project/branches" {
			return false
		}
		page := r.URL.Query().Get("page")
		reads[page]++
		if r.URL.Query().Get("per_page") != "100" {
			t.Error("branch page size not set")
		}
		if page == "2" && failSecond {
			f.apiError(w, 500, "unavailable")
			return true
		}
		if reads[page] > 1 && r.Header.Get("If-None-Match") == "" {
			t.Errorf("page %q not conditionally revalidated", page)
		}
		if page == "" {
			w.Header().Set("Link", `<`+f.server.URL+`/api/v3/repos/acme/project/branches?per_page=100&page=2>; rel="next"`)
			f.json(w, []map[string]string{{"name": "main"}})
		} else {
			f.json(w, []map[string]string{{"name": "release/v1"}})
		}
		return true
	}
	for range 2 {
		branches, err := f.driver.ListBranches(t.Context(), testRepo)
		if err != nil || strings.Join(branches, ",") != "main,release/v1" {
			t.Fatalf("branches=%v error=%v", branches, err)
		}
	}
	if f.notModified != 2 {
		t.Fatalf("expected both pages to return 304: %d", f.notModified)
	}
	f.mu.Lock()
	failSecond = true
	f.mu.Unlock()
	branches, err := f.driver.ListBranches(t.Context(), testRepo)
	if err == nil || branches != nil {
		t.Fatal("partial listing returned as success")
	}
	if reads[""] != 3 || reads["2"] != 3 {
		t.Fatalf("pages not revalidated: %v", reads)
	}
}
