package github

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

func TestPRMetadataThreeValuedMergeabilityAndLabels(t *testing.T) {
	for _, mergeable := range []*bool{nil, new(false), new(true)} {
		f := newAPI(t)
		f.prs[1].Mergeable = mergeable
		f.prs[1].Labels = []*gh.Label{{Name: "ready"}, {Name: "backend"}}
		pr, err := f.driver.PullRequest(t.Context(), testRepo, 1)
		must(t, err)
		if pr.ConflictKnown != (mergeable != nil) || mergeable != nil && pr.Conflict == *mergeable || !pr.LabelsKnown || !reflect.DeepEqual(pr.Labels, []string{"ready", "backend"}) {
			t.Fatalf("metadata: %+v", pr)
		}
		f.prs[1].Labels = []*gh.Label{}
		pr, err = f.driver.PullRequest(t.Context(), testRepo, 1)
		must(t, err)
		if !pr.LabelsKnown || len(pr.Labels) != 0 {
			t.Fatalf("observed empty labels became unknown: %+v", pr)
		}
	}
}

func TestChangedFilesPaginationAndCap(t *testing.T) {
	for _, tc := range []struct {
		name            string
		count, declared int
		known, fails    bool
	}{
		{"empty", 0, 0, true, false},
		{"multiple pages", 5, 5, true, false},
		{"at cap", forge.MaxChangedFiles, forge.MaxChangedFiles, true, false},
		{"over cap declared", forge.MaxChangedFiles + 1, forge.MaxChangedFiles + 1, false, false},
		{"over cap undeclared", forge.MaxChangedFiles + 1, -1, false, false},
		{"incomplete list", 2, 5, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			if tc.declared >= 0 {
				f.prs[1].ChangedFiles = new(tc.declared)
			}
			calls := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/pulls/1/files") {
					return false
				}
				calls++
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				pageSize := 100
				if tc.count < 10 {
					pageSize = 2
				}
				list := []map[string]any{}
				for i := (page - 1) * pageSize; i < min(page*pageSize, tc.count); i++ {
					list = append(list, map[string]any{"filename": fmt.Sprintf("src/%04d.go", i), "status": "modified"})
				}
				if page*pageSize < tc.count {
					w.Header().Set("Link", fmt.Sprintf("<%s/api/v3/repos/acme/project/pulls/1/files?page=%d>; rel=\"next\"", f.server.URL, page+1))
				}
				f.json(w, list)
				return true
			}
			pr, err := f.driver.PullRequest(t.Context(), testRepo, 1)
			if (err != nil) != tc.fails {
				t.Fatalf("error=%v, want failure=%v", err, tc.fails)
			}
			if tc.fails {
				return
			}
			if pr.FilesKnown != tc.known || tc.known && len(pr.Files) != tc.count || !tc.known && len(pr.Files) != 0 {
				t.Fatalf("truncated files became data: known=%v files=%d", pr.FilesKnown, len(pr.Files))
			}
			if tc.declared > forge.MaxChangedFiles && calls != 0 || calls > 10 {
				t.Fatalf("unbounded file reads: %d", calls)
			}
		})
	}
}

func TestChangedFilesRejectHeadOrBaseMovementAndReadFailures(t *testing.T) {
	for _, mode := range []string{"head", "base", "page failure", "repeated page"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/files") {
					return false
				}
				switch mode {
				case "head":
					f.prs[1].Head.SHA = new("changed-head")
				case "base":
					f.prs[1].Base.SHA = new("changed-base")
				case "page failure":
					f.apiError(w, 403, "private error")
					return true
				case "repeated page":
					w.Header().Set("Link", "<"+f.server.URL+"/api/v3/repos/acme/project/pulls/1/files?page=1>; rel=\"next\"")
				}
				f.json(w, []map[string]any{{"filename": "a.go"}})
				return true
			}
			_, err := f.driver.PullRequest(t.Context(), testRepo, 1)
			if err == nil || strings.Contains(err.Error(), "private error") {
				t.Fatalf("mixed/failed observation accepted: %v", err)
			}
		})
	}
}
