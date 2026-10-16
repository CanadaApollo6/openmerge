package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	gh "github.com/google/go-github/v93/github"
)

var testRepo = forge.RepoRef{Path: "acme/project"}
var epoch = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func testPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

type apiFixture struct {
	t               *testing.T
	server          *httptest.Server
	app             *App
	driver          *Driver
	mu              sync.Mutex
	clock           time.Time
	prs             map[int]*gh.PullRequest
	reviews         map[int][]*gh.PullRequestReview
	permissions     map[string]string
	permissionCalls map[string]int
	runs            map[string][]*gh.CheckRun
	statuses        map[string][]*gh.RepoStatus
	comments        map[int][]*gh.IssueComment
	refs            map[string]bool
	calls           map[string]int
	next            int64
	tokens          int
	override        func(http.ResponseWriter, *http.Request) bool
}

func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	f := &apiFixture{t: t, clock: epoch, prs: make(map[int]*gh.PullRequest), reviews: make(map[int][]*gh.PullRequestReview), permissions: map[string]string{"writer": "write", "reader": "read"}, permissionCalls: make(map[string]int), runs: make(map[string][]*gh.CheckRun), statuses: make(map[string][]*gh.RepoStatus), comments: make(map[int][]*gh.IssueComment), refs: make(map[string]bool), calls: make(map[string]int), next: 100}
	for _, n := range []int{1, 2} {
		headRepo := int64(50)
		if n == 1 {
			headRepo = 51
		}
		sha := fmt.Sprintf("%040x", n)
		f.prs[n] = &gh.PullRequest{Number: new(n), State: new("open"), Head: &gh.PullRequestBranch{SHA: &sha, Repo: &gh.Repository{ID: &headRepo}}, Base: &gh.PullRequestBranch{Ref: new("main"), Repo: &gh.Repository{ID: new(int64(50))}}}
		user := "writer"
		if n == 2 {
			user = "reader"
		}
		f.reviews[n] = []*gh.PullRequestReview{{ID: new(int64(n)), User: &gh.User{Login: &user}, State: new("APPROVED"), CommitID: &sha}}
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	app, err := New(Config{AppID: 42, PrivateKey: testPEM(t), APIBaseURL: f.server.URL + "/api/v3/", GitBaseURL: "https://github.test/"})
	must(t, err)
	f.app = app
	app.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
	f.driver, err = app.Installation(context.Background(), 7)
	must(t, err)
	return f
}
func (f *apiFixture) json(w http.ResponseWriter, v any) {
	f.t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Error(err)
	}
}
func (f *apiFixture) id() int64 { f.next++; return f.next }
func (f *apiFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	f.calls[r.Method+" "+path]++
	if f.override != nil && f.override(w, r) {
		return
	}
	if path == "app" {
		f.json(w, map[string]any{"id": 42, "slug": "queue-app"})
		return
	}
	if strings.HasPrefix(path, "app/installations/") {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			f.t.Error("token endpoint did not use JWT")
		}
		f.tokens++
		f.json(w, map[string]any{"token": fmt.Sprintf("secret-installation-%d", f.tokens), "expires_at": f.clock.Add(time.Hour).Format(time.RFC3339)})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer secret-installation-") {
		f.t.Error("API request missing installation credential")
	}
	path = strings.TrimPrefix(path, "repos/acme/project/")
	parts := strings.Split(path, "/")
	number := 0
	if len(parts) > 1 {
		number, _ = strconv.Atoi(parts[1])
	}
	switch {
	case len(parts) == 2 && parts[0] == "pulls":
		pr := f.prs[number]
		if pr == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == "PATCH" {
			var update gh.PullRequest
			must(f.t, json.NewDecoder(r.Body).Decode(&update))
			pr.State = update.State
		}
		f.json(w, pr)
	case len(parts) == 3 && parts[0] == "pulls" && parts[2] == "reviews":
		f.json(w, paginate(f, r, w, f.reviews[number]))
	case len(parts) == 3 && parts[0] == "collaborators":
		user := parts[1]
		f.permissionCalls[user]++
		permission := f.permissions[user]
		if permission == "" {
			http.NotFound(w, r)
			return
		}
		f.json(w, map[string]string{"permission": permission})
	case len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-runs":
		if r.URL.Query().Get("filter") != "all" {
			f.t.Error("check list omitted filter=all")
		}
		f.json(w, map[string]any{"check_runs": paginate(f, r, w, f.runs[parts[1]])})
	case len(parts) == 3 && parts[0] == "commits" && parts[2] == "statuses":
		f.json(w, paginate(f, r, w, f.statuses[parts[1]]))
	case path == "check-runs" && r.Method == "POST":
		var run gh.CheckRun
		must(f.t, json.NewDecoder(r.Body).Decode(&run))
		run.ID = new(f.id())
		run.App = &gh.App{ID: new(int64(42))}
		f.runs[run.GetHeadSHA()] = append(f.runs[run.GetHeadSHA()], &run)
		f.json(w, run)
	case len(parts) == 2 && parts[0] == "check-runs" && r.Method == "PATCH":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		for _, runs := range f.runs {
			for _, run := range runs {
				if run.GetID() == id {
					var update gh.CheckRun
					must(f.t, json.NewDecoder(r.Body).Decode(&update))
					run.Status, run.Conclusion, run.Output = update.Status, update.Conclusion, update.Output
					f.json(w, run)
					return
				}
			}
		}
		http.NotFound(w, r)
	case len(parts) == 3 && parts[0] == "issues" && parts[2] == "comments":
		if r.Method == "POST" {
			var c gh.IssueComment
			must(f.t, json.NewDecoder(r.Body).Decode(&c))
			c.ID = new(f.id())
			c.User = &gh.User{Login: new("queue-app[bot]"), Type: new("Bot")}
			f.comments[number] = append(f.comments[number], &c)
			f.json(w, c)
		} else {
			f.json(w, paginate(f, r, w, f.comments[number]))
		}
	case strings.HasPrefix(path, "git/refs/") && r.Method == "DELETE":
		ref := strings.TrimPrefix(path, "git/refs/heads/")
		if !f.refs[ref] {
			w.WriteHeader(422)
			f.json(w, map[string]string{"message": "Reference does not exist"})
			return
		}
		delete(f.refs, ref)
		w.WriteHeader(204)
	case path == "contents/.openmerge.yml":
		f.json(w, map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("queue: main\n"))})
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.String())
		http.NotFound(w, r)
	}
}

// Force two items per page even when the client asks for 100, exercising Link.
func paginate[T any](f *apiFixture, r *http.Request, w http.ResponseWriter, list []T) []T {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	start := min((page-1)*2, len(list))
	end := min(start+2, len(list))
	if end < len(list) {
		next := *r.URL
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf("<%s%s>; rel=\"next\"", f.server.URL, next.String()))
	}
	return slices.Clone(list[start:end])
}
func (f *apiFixture) env() forgetest.Env {
	e := forgetest.Env{Driver: f.driver, Repo: testRepo, Base: "main", BaseSHA: strings.Repeat("a", 40)}
	for _, n := range []int{1, 2} {
		pr := f.prs[n]
		sha := pr.GetHead().GetSHA()
		e.PRs = append(e.PRs, forge.BuildPR{Number: n, HeadSHA: sha})
		user := "writer"
		if n == 2 {
			user = "reader"
		}
		e.Observations = append(e.Observations, forge.PullRequest{Number: n, HeadSHA: sha, HeadInBaseRepo: n == 2, Approvals: []forge.Approval{{User: user, SHA: sha, CanWrite: n == 1}}})
	}
	e.NewBase = func(*testing.T) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		ref := fmt.Sprintf("contract-%d", f.id())
		f.refs[ref] = true
		return ref
	}
	e.RefExists = func(ref string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.refs[ref] }
	e.PushPR = func(pr int) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		sha := fmt.Sprintf("%040x", f.id())
		f.prs[pr].Head.SHA = &sha
		return sha
	}
	e.AddForeignComment = func(pr int, body string) {
		f.comments[pr] = append(f.comments[pr], &gh.IssueComment{Body: &body, User: &gh.User{Login: new("attacker"), Type: new("User")}})
	}
	e.AddCheckRun = func(sha, name string, state forge.CheckState) {
		status, conclusion := "completed", string(state)
		if state == forge.Pending {
			status = "in_progress"
			conclusion = ""
		}
		f.runs[sha] = append(f.runs[sha], &gh.CheckRun{ID: new(f.id()), Name: &name, Status: &status, Conclusion: &conclusion, App: &gh.App{ID: new(int64(99))}})
	}
	e.Statuses = func(pr int) map[string][]forge.Status {
		result := make(map[string][]forge.Status)
		for sha, runs := range f.runs {
			for _, run := range runs {
				if run.GetExternalID() != fmt.Sprintf("openmerge/pr/%d", pr) {
					continue
				}
				state, reason, _ := strings.Cut(run.GetOutput().GetTitle(), ": ")
				summary := run.GetOutput().GetSummary()
				if summary == state {
					summary = ""
				}
				result[sha] = append(result[sha], forge.Status{State: forge.StatusState(state), Reason: reason, Summary: summary})
			}
		}
		return result
	}
	e.Comments = func(pr int) map[string]string {
		result := make(map[string]string)
		for _, c := range f.comments[pr] {
			if c.GetUser().GetLogin() != "queue-app[bot]" {
				continue
			}
			_, after, ok := strings.Cut(c.GetBody(), "<!-- openmerge:key=")
			if ok {
				key, _, _ := strings.Cut(after, " -->")
				result[key] = c.GetBody()
			}
		}
		return result
	}
	e.Landed = func(pr int, sha string) bool {
		comments := e.Comments(pr)
		return len(comments) == 1 && strings.Contains(comments["landed/"+sha], "commit/"+sha)
	}
	return e
}
func TestDriverContract(t *testing.T) {
	forgetest.RunDriver(t, func(t *testing.T) forgetest.Env { return newAPI(t).env() })
}
