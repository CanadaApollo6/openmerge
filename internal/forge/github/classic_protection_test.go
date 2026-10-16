package github

import (
	"net/http"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

func TestClassicProtectionProblemsAndAppIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		edit       func(*gh.Protection)
		needsForce bool
	}{
		{"unrestricted", "", func(*gh.Protection) {}, false},
		{"reviews", "reviews", func(p *gh.Protection) { p.RequiredPullRequestReviews = &gh.PullRequestReviewsEnforcement{} }, false},
		{"review bypass is app ID", "", func(p *gh.Protection) {
			p.RequiredPullRequestReviews = &gh.PullRequestReviewsEnforcement{BypassPullRequestAllowances: &gh.BypassPullRequestAllowances{Apps: []*gh.App{{ID: new(int64(42))}}}}
		}, false},
		{"installation ID is not bypass", "reviews", func(p *gh.Protection) {
			p.RequiredPullRequestReviews = &gh.PullRequestReviewsEnforcement{BypassPullRequestAllowances: &gh.BypassPullRequestAllowances{Apps: []*gh.App{{ID: new(int64(7))}}}}
		}, false},
		{"checks", "status_checks", func(p *gh.Protection) {
			p.RequiredStatusChecks = &gh.RequiredStatusChecks{Contexts: new([]string{"ci"})}
		}, false},
		{"app checks", "status_checks", func(p *gh.Protection) {
			p.RequiredStatusChecks = &gh.RequiredStatusChecks{Checks: new([]*gh.RequiredStatusCheck{{Context: "ci", AppID: new(int64(42))}})}
		}, false},
		{"signed commits", "signed_commits", func(p *gh.Protection) { p.RequiredSignatures = &gh.SignaturesProtectedBranch{Enabled: new(true)} }, false},
		{"linear history", "linear_history", func(p *gh.Protection) { p.RequireLinearHistory = &gh.RequireLinearHistory{Enabled: true} }, false},
		{"push restrictions", "push_restrictions", func(p *gh.Protection) { p.Restrictions = &gh.BranchRestrictions{Apps: []*gh.App{{ID: new(int64(7))}}} }, false},
		{"app push allowed", "", func(p *gh.Protection) { p.Restrictions = &gh.BranchRestrictions{Apps: []*gh.App{{ID: new(int64(42))}}} }, false},
		{"locked branch", "locked", func(p *gh.Protection) { p.LockBranch = &gh.LockBranch{Enabled: new(true)} }, false},
		{"force not needed", "", func(p *gh.Protection) { p.AllowForcePushes = &gh.AllowForcePushes{Enabled: false} }, false},
		{"force needed", "force_push", func(*gh.Protection) {}, true},
		{"force allowed", "", func(p *gh.Protection) { p.AllowForcePushes = &gh.AllowForcePushes{Enabled: true} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &gh.Protection{}
			tc.edit(p)
			problems := classicProtectionProblems(p, "openmerge/fence/release/v1", 42, true, tc.needsForce)
			if tc.code == "" && len(problems) != 0 || tc.code != "" && (len(problems) != 1 || !strings.Contains(problems[0], "[classic_"+tc.code+"]") || !strings.Contains(problems[0], "openmerge/fence/release/v1")) {
				t.Fatalf("problems=%v", problems)
			}
		})
	}
}

func TestClassicProtectionInstallationReadsAndUnreadableState(t *testing.T) {
	for _, mode := range []string{"none", "blocked fence", "forbidden", "not found", "missing signatures", "signatures forbidden"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			reads := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "/branches/") {
					return false
				}
				reads++
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer secret-installation-1" {
					t.Errorf("wrong protection credentials/method: %s %s", r.Method, r.Header.Get("Authorization"))
				}
				if strings.HasSuffix(r.URL.Path, "/required_signatures") {
					if mode == "signatures forbidden" {
						f.apiError(w, 403, "secret policy body")
					} else {
						f.json(w, map[string]bool{"enabled": true})
					}
					return true
				}
				switch mode {
				case "none":
					f.apiError(w, 404, "Branch not protected")
				case "forbidden":
					f.apiError(w, 403, "secret policy body")
				case "not found":
					f.apiError(w, 404, "Not Found")
				case "missing signatures", "signatures forbidden":
					f.json(w, map[string]any{"required_pull_request_reviews": nil, "required_status_checks": nil, "restrictions": nil})
				default:
					if strings.Contains(r.URL.Path, "openmerge/fence/release/v1") {
						if !strings.Contains(r.URL.EscapedPath(), "openmerge%2Ffence%2Frelease%2Fv1") {
							t.Error("branch was not escaped")
						}
						f.json(w, map[string]any{"required_signatures": map[string]bool{"enabled": false}, "restrictions": map[string]any{"apps": []any{}}})
					} else {
						f.apiError(w, 404, "Branch not protected")
					}
				}
				return true
			}
			problems, err := f.driver.classicProtection(t.Context(), "acme", "project", "release/v1", forge.Merge)
			if mode == "forbidden" || mode == "not found" || mode == "signatures forbidden" {
				if err == nil || strings.Contains(err.Error(), "secret policy body") || len(problems) > 0 {
					t.Fatalf("unreadable treated as absent or partial result exposed: %v %v", problems, err)
				}
				return
			}
			must(t, err)
			if reads < 2 || mode == "none" && len(problems) != 0 || mode == "blocked fence" && (len(problems) != 1 || !strings.Contains(problems[0], "classic_push_restrictions")) || mode == "missing signatures" && (reads != 4 || len(problems) != 2) {
				t.Fatalf("reads=%d problems=%v", reads, problems)
			}
		})
	}
}

func TestClassicLinearHistoryUsesConfiguredCommitShape(t *testing.T) {
	for _, method := range []forge.MergeMethod{"", forge.Merge, forge.Squash, forge.Rebase} {
		t.Run(string(method), func(t *testing.T) {
			f := newAPI(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.Contains(r.URL.Path, "/branches/") {
					return false
				}
				f.json(w, map[string]any{"required_signatures": map[string]bool{"enabled": false}, "required_linear_history": map[string]bool{"enabled": true}, "allow_force_pushes": map[string]bool{"enabled": false}})
				return true
			}
			problems, _, err := f.driver.ValidateClassicProtection(t.Context(), testRepo, "main", method)
			if method == "" {
				if err == nil {
					t.Fatal("unknown method cleared a known block")
				}
				return
			}
			must(t, err)
			if method == forge.Merge && (len(problems) != 1 || !strings.Contains(problems[0], "classic_linear_history") || strings.Contains(problems[0], "fence")) || method != forge.Merge && len(problems) != 0 {
				t.Fatalf("method=%s problems=%v", method, problems)
			}
		})
	}
}
