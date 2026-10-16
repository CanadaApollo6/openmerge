package github

import (
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v93/github"
)

func TestCommitAuthorsPaginationAndRequestCost(t *testing.T) {
	f := newAPI(t)
	f.commits[2] = []*gh.RepositoryCommit{
		{SHA: new("one"), Author: &gh.User{Login: new("contributor"), ID: new(int64(12))}},
		{SHA: new("two")},
		{SHA: new("three"), Author: &gh.User{Login: new("other"), ID: new(int64(13))}},
	}
	_, err := f.driver.PullRequest(t.Context(), testRepo, 2)
	must(t, err)
	if f.calls["GET repos/acme/project/pulls/2/commits"] != 0 {
		t.Fatal("reconciliation fetched commit authors")
	}
	authors, err := f.driver.CommitAuthors(t.Context(), testRepo, 2)
	must(t, err)
	if len(authors) != 3 || authors[0].ID != 12 || authors[0].Login != "contributor" || authors[1].ID != 0 || authors[1].Login != "" || authors[2].SHA != "three" {
		t.Fatalf("linked authors lost: %+v", authors)
	}
	if got := f.calls["GET repos/acme/project/pulls/2/commits"]; got != 2 {
		t.Fatalf("request cost = %d, want two pages", got)
	}
	f.commits[2] = f.commits[2][:1]
	before := f.calls["GET repos/acme/project/pulls/2/commits"]
	_, err = f.driver.CommitAuthors(t.Context(), testRepo, 2)
	must(t, err)
	t.Logf("single-page PR: %d additional commit-list request", f.calls["GET repos/acme/project/pulls/2/commits"]-before)
	if f.calls["GET repos/acme/project/pulls/2/commits"]-before != 1 {
		t.Fatal("unexpected single-page request cost")
	}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/commits") {
			f.apiError(w, 503, "unavailable")
			return true
		}
		return false
	}
	if _, err := f.driver.CommitAuthors(t.Context(), testRepo, 2); err == nil {
		t.Fatal("author read failure ignored")
	}
}

func TestHeadBranchDeletionGuards(t *testing.T) {
	for _, scenario := range []string{"eligible", "fork", "base", "default", "protected", "unknown-protection", "open", "changed-head", "changed-branch", "missing", "targeted", "shared-head", "fork-namesake", "closed-shared-head", "repo-error", "branch-error", "list-error", "head-list-error", "reserved"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAPI(t)
			pr := f.prs[2]
			pr.State = new("closed")
			pr.Head.Ref = new("feature/topic")
			sha := pr.GetHead().GetSHA()
			switch scenario {
			case "fork":
				pr.Head.Repo.ID = new(int64(51))
			case "base":
				pr.Head.Ref = new("main")
			case "default":
				pr.Head.Ref = new("default")
			case "open":
				pr.State = new("open")
			case "changed-head":
				pr.Head.SHA = new("newer")
			case "reserved":
				pr.Head.Ref = new("openmerge/fence/main")
			case "targeted":
				for _, n := range []int{1, 3, 4} {
					f.prs[n] = &gh.PullRequest{Number: new(n), State: new("open"), Base: &gh.PullRequestBranch{Ref: new("feature/topic")}}
				}
			case "shared-head", "fork-namesake", "closed-shared-head":
				other := f.prs[1]
				other.Head.Ref = new("feature/topic")
				other.Head.Label = new("acme:feature/topic")
				other.Head.Repo.ID = new(int64(50))
				other.Base.Ref = new("release")
				if scenario == "fork-namesake" {
					other.Head.Label = new("contributor:feature/topic")
					other.Head.Repo.ID = new(int64(51))
				}
				if scenario == "closed-shared-head" {
					other.State = new("closed")
				}
			}
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
				if path == "repos/acme/project" {
					if scenario == "repo-error" {
						f.apiError(w, 503, "unavailable")
					} else {
						f.json(w, map[string]any{"id": 50, "default_branch": "default"})
					}
					return true
				}
				if strings.Contains(path, "/branches/") {
					if scenario == "missing" {
						f.apiError(w, 404, "Not Found")
						return true
					}
					if scenario == "branch-error" {
						f.apiError(w, 403, "Forbidden")
						return true
					}
					b := map[string]any{"name": pr.GetHead().GetRef(), "protected": scenario == "protected", "commit": map[string]any{"sha": sha}}
					if scenario == "unknown-protection" {
						delete(b, "protected")
					}
					if scenario == "changed-branch" {
						b["commit"] = map[string]any{"sha": "newer"}
					}
					f.json(w, b)
					return true
				}
				if path == "repos/acme/project/pulls" && scenario == "list-error" {
					f.apiError(w, 503, "unavailable")
					return true
				}
				if path == "repos/acme/project/pulls" && r.URL.Query().Get("head") != "" {
					if r.URL.Query().Get("head") != "acme:feature/topic" || r.URL.Query().Get("base") != "" {
						t.Errorf("head usage must be repository-qualified across all bases: %s", r.URL.RawQuery)
					}
					if scenario == "head-list-error" {
						f.apiError(w, 503, "unavailable")
						return true
					}
				}
				return false
			}
			branch, err := f.driver.HeadBranchForDeletion(t.Context(), testRepo, 2, sha, "main")
			wantError := strings.HasSuffix(scenario, "-error") || scenario == "open"
			if (err != nil) != wantError {
				t.Fatalf("branch=%q error=%v", branch, err)
			}
			want := ""
			if scenario == "eligible" || scenario == "fork-namesake" || scenario == "closed-shared-head" {
				want = "feature/topic"
			}
			if branch != want {
				t.Fatalf("branch=%q, want %q", branch, want)
			}
			if scenario == "targeted" && f.calls["GET repos/acme/project/pulls"] != 2 {
				t.Fatal("target PR check did not paginate")
			}
			for call := range f.calls {
				if strings.HasPrefix(call, "DELETE ") {
					t.Fatalf("eligibility read mutated refs: %s", call)
				}
			}
		})
	}
}
