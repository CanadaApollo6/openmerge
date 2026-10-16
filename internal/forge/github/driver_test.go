package github

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	gh "github.com/google/go-github/v93/github"
)

func TestApprovalRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		states []string
		want   string
	}{{"stale", []string{"APPROVED"}, "old"}, {"dismissed", []string{"DISMISSED"}, ""}, {"changes supersede", []string{"APPROVED", "CHANGES_REQUESTED"}, ""}, {"dismissal supersedes", []string{"APPROVED", "DISMISSED"}, ""}, {"new approval", []string{"CHANGES_REQUESTED", "APPROVED"}, "old"}, {"comment does not revoke", []string{"APPROVED", "COMMENTED", "PENDING"}, "old"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			f.reviews[1] = nil
			for i, state := range tc.states {
				f.reviews[1] = append(f.reviews[1], &gh.PullRequestReview{ID: new(int64(100 + i)), State: &state, CommitID: new("old"), User: &gh.User{Login: new("writer")}})
			}
			// Deliberately reverse API ordering, including across page boundaries.
			for i, j := 0, len(f.reviews[1])-1; i < j; i, j = i+1, j-1 {
				f.reviews[1][i], f.reviews[1][j] = f.reviews[1][j], f.reviews[1][i]
			}
			got, err := f.driver.PullRequest(context.Background(), testRepo, 1)
			must(t, err)
			if tc.want == "" {
				if len(got.Approvals) != 0 {
					t.Fatalf("revoked approval counted: %+v", got.Approvals)
				}
			} else if !reflect.DeepEqual(got.Approvals, []forge.Approval{{User: "writer", SHA: tc.want, CanWrite: true}}) {
				t.Fatalf("approval=%+v", got.Approvals)
			}
		})
	}
}
func TestPermissions(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	for _, tc := range []struct {
		permission string
		want       bool
	}{{"read", false}, {"triage", false}, {"write", true}, {"admin", true}, {"maintain", true}, {"custom-unknown", false}, {"", false}} {
		f.permissions["writer"] = tc.permission
		f.clock = f.clock.Add(time.Minute)
		got, err := f.driver.CanWrite(ctx, testRepo, "writer")
		must(t, err)
		if got != tc.want {
			t.Fatalf("permission %q returned %v", tc.permission, got)
		}
	}
	f.permissions["writer"] = "write"
	f.clock = f.clock.Add(time.Minute)
	got, err := f.driver.CanWrite(ctx, testRepo, "writer")
	must(t, err)
	if !got {
		t.Fatal("write denied")
	}
	count := f.permissionCalls["writer"]
	f.permissions["writer"] = "read"
	got, err = f.driver.CanWrite(ctx, testRepo, "writer")
	must(t, err)
	if !got || f.permissionCalls["writer"] != count {
		t.Fatal("permission TTL not cached")
	}
	f.clock = f.clock.Add(time.Minute)
	got, err = f.driver.CanWrite(ctx, testRepo, "writer")
	must(t, err)
	if got {
		t.Fatal("permission revocation not refreshed")
	}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "collaborators/") {
			w.WriteHeader(403)
			f.json(w, map[string]string{"message": "not accessible"})
			return true
		}
		return false
	}
	_, err = f.driver.CanWrite(ctx, testRepo, "uncached")
	if err == nil {
		t.Fatal("permission API failure accepted")
	}
}
func TestCheckNormalization(t *testing.T) {
	f := newAPI(t)
	sha := f.prs[1].GetHead().GetSHA()
	conclusions := []string{"success", "failure", "cancelled", "skipped", "neutral", "timed_out", "action_required", "startup_failure", "stale", "unknown"}
	want := []forge.CheckState{forge.Success, forge.Failure, forge.Cancelled, forge.Skipped, forge.Neutral, forge.Failure, forge.Failure, forge.Failure, forge.Failure, forge.Pending}
	for i, conclusion := range conclusions {
		f.runs[sha] = append(f.runs[sha], &gh.CheckRun{ID: new(int64(i + 1)), Name: new(conclusion), Status: new("completed"), Conclusion: &conclusion})
	}
	f.runs[sha] = append(f.runs[sha], &gh.CheckRun{ID: new(int64(200)), Name: new("success"), Status: new("in_progress"), Conclusion: new("success")})
	want = append(want, forge.Pending)
	// Only our own check is filtered; a different App may use the same name.
	f.runs[sha] = append(f.runs[sha], &gh.CheckRun{ID: new(int64(201)), Name: new("OpenMerge"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(int64(42))}}, &gh.CheckRun{ID: new(int64(202)), Name: new("OpenMerge"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(int64(99))}})
	want = append(want, forge.Success)
	for i, state := range []string{"success", "failure", "error", "pending", "unknown"} {
		f.statuses[sha] = append(f.statuses[sha], &gh.RepoStatus{ID: new(int64(300 + i)), Context: new("legacy"), State: &state})
	}
	want = append(want, forge.Success, forge.Failure, forge.Failure, forge.Pending, forge.Pending)
	got, err := f.driver.CheckRuns(context.Background(), testRepo, sha)
	must(t, err)
	if len(got) != len(want) {
		t.Fatalf("runs=%+v", got)
	}
	for i, run := range got {
		if run.State != want[i] {
			t.Errorf("run %d=%+v want %s", i, run, want[i])
		}
	}
	if got[10].ID <= got[0].ID {
		t.Fatal("new pending rerun did not supersede success")
	}
	if got[len(got)-1].ID != 304 {
		t.Fatal("legacy status did not retain native ordering ID")
	}
}
func TestStatusMappingsAndConcurrentWrites(t *testing.T) {
	f := newAPI(t)
	sha := f.prs[1].GetHead().GetSHA()
	ctx := context.Background()
	for _, tc := range []struct {
		state              forge.StatusState
		status, conclusion string
	}{{forge.Queued, "in_progress", ""}, {forge.Testing, "in_progress", ""}, {forge.Landing, "in_progress", ""}, {forge.Merged, "completed", "success"}, {forge.Dequeued, "completed", "cancelled"}, {forge.Blocked, "completed", "failure"}, {forge.Paused, "completed", "action_required"}, {forge.Queued, "in_progress", ""}} {
		must(t, f.driver.ReportStatus(ctx, testRepo, 1, sha, forge.Status{State: tc.state, Reason: "why", Summary: "human summary"}))
		if len(f.runs[sha]) != 1 {
			t.Fatal("duplicate check runs")
		}
		run := f.runs[sha][0]
		if run.GetStatus() != tc.status || run.GetConclusion() != tc.conclusion || run.GetOutput().GetSummary() != "human summary" || !strings.Contains(run.GetOutput().GetTitle(), "why") {
			t.Fatalf("state %s mapped to %+v", tc.state, run)
		}
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := f.driver.ReportStatus(ctx, testRepo, 2, "fresh-head", forge.Status{State: forge.Queued}); err != nil {
				t.Error(err)
			}
			if err := f.driver.Comment(ctx, testRepo, 1, "concurrent", "body"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(f.runs["fresh-head"]) != 1 || len(f.comments[1]) != 1 {
		t.Fatal("concurrent writes duplicated effects")
	}
	// New process state: rediscover persisted identity rather than trusting a cache.
	app, err := New(Config{AppID: 42, PrivateKey: testPEM(t), APIBaseURL: f.server.URL + "/api/v3/"})
	must(t, err)
	app.now = func() time.Time { return epoch }
	driver, err := app.Installation(ctx, 7)
	must(t, err)
	must(t, driver.ReportStatus(ctx, testRepo, 2, "fresh-head", forge.Status{State: forge.Blocked}))
	must(t, driver.Comment(ctx, testRepo, 1, "concurrent", "retry"))
	if len(f.runs["fresh-head"]) != 1 || len(f.comments[1]) != 1 {
		t.Fatal("restart duplicated writes")
	}
}
func TestSpoofedMarkersAndPagination(t *testing.T) {
	f := newAPI(t)
	marker := "<!-- openmerge:key=key -->"
	f.comments[1] = []*gh.IssueComment{{Body: &marker, User: &gh.User{Login: new("human"), Type: new("User")}}, {Body: &marker, User: &gh.User{Login: new("another[bot]"), Type: new("Bot")}}, {Body: &marker, User: &gh.User{Login: new("queue-app[bot]"), Type: new("User")}}}
	ctx := context.Background()
	must(t, f.driver.Comment(ctx, testRepo, 1, "key", "original"))
	must(t, f.driver.Comment(ctx, testRepo, 1, "key", "retry"))
	if len(f.comments[1]) != 4 || !strings.Contains(f.comments[1][3].GetBody(), "original") {
		t.Fatal("spoof suppressed comment or paginated dedupe failed")
	}
}
func TestDeleteRefAndReadFile(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	for _, ref := range []string{"topic", "heads/topic", "refs/heads/topic"} {
		f.refs["topic"] = true
		must(t, f.driver.DeleteRef(ctx, testRepo, ref))
		if f.refs["topic"] {
			t.Fatal("branch not deleted")
		}
		must(t, f.driver.DeleteRef(ctx, testRepo, ref))
	}
	for _, tc := range []struct {
		status    int
		message   string
		wantError bool
	}{{404, "not found", false}, {422, "Reference does not exist.", false}, {422, "Reference update failed", true}, {403, "not authorized", true}} {
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != "DELETE" {
				return false
			}
			w.WriteHeader(tc.status)
			f.json(w, map[string]string{"message": tc.message})
			return true
		}
		err := f.driver.DeleteRef(ctx, testRepo, "missing")
		if (err != nil) != tc.wantError {
			t.Fatalf("delete %d %q=%v", tc.status, tc.message, err)
		}
	}
	f.override = nil
	contents, err := f.driver.ReadFile(ctx, testRepo, "release/v1", ".openmerge.yml")
	must(t, err)
	if string(contents) != "queue: main\n" {
		t.Fatalf("contents=%q", contents)
	}
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/contents/") {
			if r.URL.Query().Get("ref") != "release/v1" {
				t.Error("ReadFile lost ref")
			}
			http.NotFound(w, r)
			return true
		}
		return false
	}
	_, err = f.driver.ReadFile(ctx, testRepo, "release/v1", ".openmerge.yml")
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 404 {
		t.Fatalf("missing config=%v", err)
	}
}
func TestRemoteAndHeadRef(t *testing.T) {
	f := newAPI(t)
	remote, auth, err := f.driver.Remote(context.Background(), testRepo)
	must(t, err)
	if remote != "https://github.test/acme/project.git" || auth.Username != "x-access-token" || auth.Password != "secret-installation-1" || HeadRef(123) != "refs/pull/123/head" {
		t.Fatal("incorrect Git mapping or credential")
	}
	for _, repo := range []forge.RepoRef{{Path: "group/subgroup/repo"}, {Path: "owner/.."}, {Path: "owner/repo?secret"}} {
		if _, _, err = f.driver.Remote(context.Background(), repo); err == nil {
			t.Fatal("invalid GitHub repository accepted")
		}
	}
	app, err := New(Config{AppID: 42, PrivateKey: testPEM(t), APIBaseURL: "https://ghes.example/api/v3/"})
	must(t, err)
	if app.git.String() != "https://ghes.example/" {
		t.Fatal("GHES Git URL derived incorrectly")
	}
}

func TestCheckNamesAcrossAPIsFailClosed(t *testing.T) {
	f := newAPI(t)
	f.runs["sha"] = []*gh.CheckRun{{ID: new(int64(9999)), Name: new("CI"), Status: new("completed"), Conclusion: new("success")}}
	f.statuses["sha"] = []*gh.RepoStatus{{ID: new(int64(1)), Context: new("CI"), State: new("pending")}}
	_, err := f.driver.CheckRuns(context.Background(), testRepo, "sha")
	var collision *CheckNameCollisionError
	if !errors.As(err, &collision) || collision.Name != "CI" {
		t.Fatalf("cross-API native IDs were compared unsafely: %v", err)
	}
}
