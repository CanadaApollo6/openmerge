package github

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
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

func TestChangedFilesRejectMetadataChangesDuringPagination(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*gh.PullRequest)
	}{
		{"label removed", func(pr *gh.PullRequest) { pr.Labels = []*gh.Label{} }},
		{"labels unknown", func(pr *gh.PullRequest) { pr.Labels = nil }},
		{"title", func(pr *gh.PullRequest) { pr.Title = new("new title") }},
		{"body", func(pr *gh.PullRequest) { pr.Body = new("new body") }},
		{"draft", func(pr *gh.PullRequest) { pr.Draft = new(true) }},
		{"mergeable", func(pr *gh.PullRequest) { pr.Mergeable = new(false) }},
		{"mergeable unknown", func(pr *gh.PullRequest) { pr.Mergeable = nil }},
		{"author", func(pr *gh.PullRequest) { pr.User.Login = new("other") }},
		{"head branch", func(pr *gh.PullRequest) { pr.Head.Label = new("other:branch") }},
		{"head repository", func(pr *gh.PullRequest) { pr.Head.Repo.ID = pr.Base.Repo.ID }},
		{"base branch", func(pr *gh.PullRequest) { pr.Base.Ref = new("release") }},
		{"closed", func(pr *gh.PullRequest) { pr.State = new("closed") }},
		{"file count", func(pr *gh.PullRequest) { pr.ChangedFiles = new(2) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			f.prs[1].Labels = []*gh.Label{{Name: "ready"}}
			f.prs[1].Mergeable = new(true)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/pulls/1/files") {
					return false
				}
				tc.edit(f.prs[1])
				f.json(w, []map[string]any{{"filename": "a.go"}})
				return true
			}
			if pr, err := f.driver.PullRequest(t.Context(), testRepo, 1); err == nil || !strings.Contains(err.Error(), "changed during file observation") || pr.LabelsKnown {
				t.Fatalf("mixed metadata observation accepted: %+v %v", pr, err)
			}
		})
	}
}

func TestRenamedFilesUseDistinctUnionForConditions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		chain bool
		known bool
		paths int
	}{
		{"workflow rename", 1, false, true, 2},
		{"500 renames at cap", 500, false, true, 1000},
		{"501 renames exceed cap", 501, false, false, 0},
		{"overlapping old and new paths", 501, true, true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			f.prs[1].ChangedFiles = new(tc.count)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/pulls/1/files") {
					return false
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				list := []map[string]any{}
				for i := (page - 1) * 100; i < min(page*100, tc.count); i++ {
					old, filename := fmt.Sprintf("old/%d", i), fmt.Sprintf("new/%d", i)
					if tc.count == 1 {
						old, filename = ".github/workflows/ci.yml", "docs/ci.yml"
					}
					if tc.chain {
						old, filename = fmt.Sprintf("path/%d", i), fmt.Sprintf("path/%d", i+1)
					}
					list = append(list, map[string]any{"status": "renamed", "filename": filename, "previous_filename": old})
				}
				if page*100 < tc.count {
					w.Header().Set("Link", fmt.Sprintf("<%s/api/v3/repos/acme/project/pulls/1/files?page=%d>; rel=\"next\"", f.server.URL, page+1))
				}
				f.json(w, list)
				return true
			}
			pr, err := f.driver.PullRequest(t.Context(), testRepo, 1)
			must(t, err)
			if pr.FilesKnown != tc.known || len(pr.Files) != tc.paths {
				t.Fatalf("rename union: known=%v paths=%d", pr.FilesKnown, len(pr.Files))
			}
			values := condition.Snapshot{}
			if pr.FilesKnown {
				values["files"] = condition.Strings(pr.Files...)
			}
			expression, err := condition.Parse("files glob .github/workflows/**")
			must(t, err)
			met, _ := expression.Evaluate(values)
			negated, _ := condition.Not(expression).Evaluate(values)
			if tc.count == 1 && (!met || negated) {
				t.Fatal("renaming a workflow bypassed its file condition")
			}
			if !tc.known {
				for _, text := range []string{"files glob new/**", "files != .github/workflows/ci.yml", "#files > 0"} {
					expression, err := condition.Parse(text)
					must(t, err)
					met, _ := expression.Evaluate(values)
					negated, _ := condition.Not(expression).Evaluate(values)
					if met || negated {
						t.Fatalf("oversized rename union made a decision: %s", text)
					}
				}
			}
		})
	}
}

func TestPullRequestMetadataSkipsFileList(t *testing.T) {
	f := newAPI(t)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/files") {
			t.Error("metadata read fetched files")
			f.apiError(w, 403, "files unavailable")
			return true
		}
		return false
	}
	pr, err := f.driver.PullRequestMetadata(t.Context(), testRepo, 1)
	must(t, err)
	if pr.FilesKnown || len(pr.Files) != 0 || pr.HeadSHA == "" {
		t.Fatalf("metadata-only observation: %+v", pr)
	}
}
