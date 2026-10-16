package github

import (
	"net/http"
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
