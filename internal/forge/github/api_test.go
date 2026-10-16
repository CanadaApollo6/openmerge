package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	suites          map[string][]*gh.CheckSuite
	installations   map[int64]bool
	notModified     int
	statuses        map[string][]*gh.RepoStatus
	comments        map[int][]*gh.IssueComment
	refs            map[string]bool
	calls           map[string]int
	next            int64
	tokens          int
	setupRules      map[int64]map[string]any
	manifestPEM     string
	setupWrites     int
	override        func(http.ResponseWriter, *http.Request) bool
}

func newAPI(t *testing.T) *apiFixture {
	t.Helper()
	f := &apiFixture{t: t, clock: epoch, prs: make(map[int]*gh.PullRequest), reviews: make(map[int][]*gh.PullRequestReview), permissions: map[string]string{"writer": "write", "reader": "read"}, permissionCalls: make(map[string]int), runs: make(map[string][]*gh.CheckRun), suites: make(map[string][]*gh.CheckSuite), installations: map[int64]bool{7: true, 8: true}, statuses: make(map[string][]*gh.RepoStatus), comments: make(map[int][]*gh.IssueComment), refs: make(map[string]bool), calls: make(map[string]int), next: 100}
	for _, n := range []int{1, 2} {
		headRepo := int64(50)
		if n == 1 {
			headRepo = 51
		}
		sha := fmt.Sprintf("%040x", n)
		f.prs[n] = &gh.PullRequest{Number: new(n), State: new("open"), User: &gh.User{Login: new("contributor")}, UpdatedAt: &gh.Timestamp{Time: epoch}, Head: &gh.PullRequestBranch{SHA: &sha, Repo: &gh.Repository{ID: &headRepo}}, Base: &gh.PullRequestBranch{Ref: new("main"), Repo: &gh.Repository{ID: new(int64(50))}}}
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

// Response schemas are independent REST fixtures, never SDK serialization.
func (f *apiFixture) object(name string) map[string]any {
	f.t.Helper()
	data, err := os.ReadFile("testdata/rest/" + name + ".json")
	must(f.t, err)
	var result map[string]any
	must(f.t, json.Unmarshal(data, &result))
	return result
}
func (f *apiFixture) prJSON(pr *gh.PullRequest) map[string]any {
	o := f.object("pull_request")
	o["number"], o["state"], o["draft"] = pr.GetNumber(), pr.GetState(), pr.GetDraft()
	o["user"].(map[string]any)["login"] = pr.GetUser().GetLogin()
	for label, b := range map[string]*gh.PullRequestBranch{"head": pr.GetHead(), "base": pr.GetBase()} {
		branch := o[label].(map[string]any)
		branch["ref"], branch["sha"] = b.GetRef(), b.GetSHA()
		if b.GetRepo() == nil {
			branch["repo"] = nil
		} else {
			repo := branch["repo"].(map[string]any)
			repo["id"], repo["name"], repo["full_name"] = b.GetRepo().GetID(), b.GetRepo().GetName(), b.GetRepo().GetFullName()
		}
	}
	o["updated_at"] = pr.GetUpdatedAt().Format(time.RFC3339)
	return o
}
func (f *apiFixture) runJSON(run *gh.CheckRun) map[string]any {
	o := f.object("check_run")
	o["id"], o["name"], o["head_sha"], o["external_id"] = run.GetID(), run.GetName(), run.GetHeadSHA(), run.GetExternalID()
	o["status"], o["conclusion"] = run.GetStatus(), run.GetConclusion()
	if run.App != nil {
		o["app"].(map[string]any)["id"] = run.GetApp().GetID()
	}
	if run.CheckSuite != nil {
		o["check_suite"].(map[string]any)["id"] = run.GetCheckSuite().GetID()
	}
	if run.Output != nil {
		o["output"] = map[string]any{"title": run.GetOutput().GetTitle(), "summary": run.GetOutput().GetSummary(), "annotations_count": 0}
	}
	return o
}
func (f *apiFixture) commentJSON(c *gh.IssueComment) map[string]any {
	o := f.object("comment")
	o["id"], o["body"] = c.GetID(), c.GetBody()
	o["user"].(map[string]any)["login"], o["user"].(map[string]any)["type"] = c.GetUser().GetLogin(), c.GetUser().GetType()
	if c.CreatedAt != nil {
		o["created_at"] = c.GetCreatedAt().Format(time.RFC3339)
	}
	if c.UpdatedAt != nil {
		o["updated_at"] = c.GetUpdatedAt().Format(time.RFC3339)
	}
	return o
}
func (f *apiFixture) json(w http.ResponseWriter, v any) {
	f.t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Error(err)
	}
}
func (f *apiFixture) apiError(w http.ResponseWriter, status int, message string) {
	o := f.object("error")
	o["message"], o["status"] = message, strconv.Itoa(status)
	w.WriteHeader(status)
	f.json(w, o)
}
func (f *apiFixture) id() int64 { f.next++; return f.next }
func (f *apiFixture) assertJWT(r *http.Request) {
	f.t.Helper()
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		f.t.Error("App endpoint missing JWT")
		return
	}
	decode := func(raw string) []byte {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		must(f.t, err)
		return data
	}
	var header struct{ Alg, Typ string }
	var claims struct {
		Iss      string
		Iat, Exp int64
	}
	must(f.t, json.Unmarshal(decode(parts[0]), &header))
	must(f.t, json.Unmarshal(decode(parts[1]), &claims))
	if header.Alg != "RS256" || header.Typ != "JWT" || claims.Iss != "42" || claims.Iat > f.clock.Unix() || claims.Exp <= f.clock.Unix() || claims.Exp-claims.Iat > 600 {
		f.t.Error("App endpoint received invalid JWT claims")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	must(f.t, rsa.VerifyPKCS1v15(&f.app.key.PublicKey, crypto.SHA256, digest[:], decode(parts[2])))
}
func (f *apiFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := httptest.NewRecorder()
	out.Header().Set("Content-Type", "application/json")
	out.Header().Set("X-RateLimit-Limit", "5000")
	out.Header().Set("X-RateLimit-Remaining", "4999")
	out.Header().Set("X-RateLimit-Reset", strconv.FormatInt(f.clock.Add(time.Hour).Unix(), 10))
	out.Header().Set("X-RateLimit-Resource", "core")
	f.handle(out, r)
	status := out.Code
	if r.Method == "GET" && status == 200 {
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256(append(out.Body.Bytes(), []byte(out.Header().Get("Link"))...)))
		out.Header().Set("ETag", etag)
		out.Header().Set("Cache-Control", "private, max-age=60, s-maxage=60")
		out.Header().Set("Vary", "Accept, Authorization, X-GitHub-Api-Version, Accept-Encoding, X-Requested-With")
		if r.Header.Get("If-None-Match") == etag {
			status = 304
			f.notModified++
		}
	}
	for key, values := range out.Header() {
		w.Header()[key] = values
	}
	if status == 304 {
		w.Header().Del("Link")
	}
	w.WriteHeader(status)
	if status != 304 {
		_, err := w.Write(out.Body.Bytes())
		if err != nil {
			f.t.Error(err)
		}
	}
}
func (f *apiFixture) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v3/")
	f.calls[r.Method+" "+path]++
	if f.handleSetup(w, r, path) {
		return
	}
	if path == "app" || strings.HasPrefix(path, "app/") {
		f.assertJWT(r)
	} else if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer secret-installation-") {
		f.t.Error("API request missing installation credential")
	}
	if f.override != nil && f.override(w, r) {
		return
	}
	if path == "app" {
		f.json(w, f.object("app"))
		return
	}
	if strings.HasPrefix(path, "app/installations/") {
		parts := strings.Split(path, "/")
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		if !f.installations[id] || len(parts) != 4 || parts[3] != "access_tokens" || r.Method != "POST" {
			f.apiError(w, 404, "Not Found")
			return
		}
		f.tokens++
		o := f.object("installation_token")
		o["token"], o["expires_at"] = fmt.Sprintf("secret-installation-%d", f.tokens), f.clock.Add(time.Hour).Format(time.RFC3339)
		w.WriteHeader(201)
		f.json(w, o)
		return
	}
	path = strings.TrimPrefix(path, "repos/acme/project/")
	parts := strings.Split(path, "/")
	number := 0
	if len(parts) > 1 {
		number, _ = strconv.Atoi(parts[1])
	}
	switch {
	case path == "pulls" && r.Method == "GET":
		if r.URL.Query().Get("state") != "open" {
			f.t.Error("PR reconciliation must request open PRs")
		}
		list := []map[string]any{}
		numbers := []int{}
		for n, pr := range f.prs {
			if pr.GetState() == "open" && (r.URL.Query().Get("base") == "" || pr.GetBase().GetRef() == r.URL.Query().Get("base")) {
				numbers = append(numbers, n)
			}
		}
		slices.Sort(numbers)
		for _, n := range numbers {
			list = append(list, f.prJSON(f.prs[n]))
		}
		f.json(w, paginate(f, r, w, list))
	case len(parts) == 2 && parts[0] == "pulls":
		pr := f.prs[number]
		if pr == nil {
			f.apiError(w, 404, "Not Found")
			return
		}
		if r.Method == "PATCH" {
			var update struct{ State string }
			must(f.t, json.NewDecoder(r.Body).Decode(&update))
			pr.State = &update.State
			pr.UpdatedAt = &gh.Timestamp{Time: f.clock}
		}
		f.json(w, f.prJSON(pr))
	case len(parts) == 3 && parts[0] == "pulls" && parts[2] == "reviews":
		list := []map[string]any{}
		for _, review := range f.reviews[number] {
			o := f.object("review")
			o["id"], o["state"], o["commit_id"] = review.GetID(), review.GetState(), review.GetCommitID()
			o["user"].(map[string]any)["login"] = review.GetUser().GetLogin()
			list = append(list, o)
		}
		f.json(w, paginate(f, r, w, list))
	case len(parts) == 3 && parts[0] == "collaborators" && parts[2] == "permission":
		user := parts[1]
		f.permissionCalls[user]++
		permission := f.permissions[user]
		if permission == "" {
			f.apiError(w, 404, "Not Found")
			return
		}
		o := f.object("permission")
		o["permission"], o["role_name"] = permission, permission
		o["user"].(map[string]any)["login"] = user
		f.json(w, o)
	case len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-runs":
		if r.URL.Query().Get("filter") != "all" {
			f.t.Error("check list omitted filter=all")
		}
		list := []map[string]any{}
		for _, run := range f.runs[parts[1]] {
			list = append(list, f.runJSON(run))
		}
		f.json(w, map[string]any{"total_count": len(list), "check_runs": paginate(f, r, w, list)})
	case len(parts) == 3 && parts[0] == "commits" && parts[2] == "check-suites":
		suites, explicit := f.suites[parts[1]]
		if !explicit {
			seen := map[int64]bool{}
			for _, run := range f.runs[parts[1]] {
				id := int64(99)
				if run.CheckSuite != nil {
					id = run.GetCheckSuite().GetID()
				}
				if !seen[id] {
					suites = append(suites, &gh.CheckSuite{ID: &id, Status: new("completed")})
					seen[id] = true
				}
			}
		}
		list := []map[string]any{}
		for _, suite := range suites {
			o := f.object("check_suite")
			o["id"], o["status"], o["head_sha"] = suite.GetID(), suite.GetStatus(), parts[1]
			list = append(list, o)
		}
		f.json(w, map[string]any{"total_count": len(list), "check_suites": paginate(f, r, w, list)})
	case len(parts) == 3 && parts[0] == "commits" && parts[2] == "statuses":
		list := []map[string]any{}
		for _, status := range f.statuses[parts[1]] {
			o := f.object("status")
			o["id"], o["context"], o["state"] = status.GetID(), status.GetContext(), status.GetState()
			if status.Creator != nil {
				o["creator"].(map[string]any)["login"] = status.GetCreator().GetLogin()
			}
			list = append(list, o)
		}
		f.json(w, paginate(f, r, w, list))
	case path == "check-runs" && r.Method == "POST":
		var input struct {
			Name               string
			HeadSHA            string `json:"head_sha"`
			ExternalID         string `json:"external_id"`
			Status, Conclusion *string
			Output             *gh.CheckRunOutput
		}
		must(f.t, json.NewDecoder(r.Body).Decode(&input))
		run := &gh.CheckRun{ID: new(f.id()), Name: &input.Name, HeadSHA: &input.HeadSHA, ExternalID: &input.ExternalID, Status: input.Status, Conclusion: input.Conclusion, Output: input.Output, App: &gh.App{ID: new(int64(42))}}
		f.runs[run.GetHeadSHA()] = append(f.runs[run.GetHeadSHA()], run)
		w.WriteHeader(201)
		f.json(w, f.runJSON(run))
	case len(parts) == 2 && parts[0] == "check-runs" && r.Method == "PATCH":
		id, _ := strconv.ParseInt(parts[1], 10, 64)
		for _, runs := range f.runs {
			for _, run := range runs {
				if run.GetID() == id {
					var update struct {
						Status, Conclusion *string
						Output             *gh.CheckRunOutput
					}
					must(f.t, json.NewDecoder(r.Body).Decode(&update))
					run.Status, run.Conclusion, run.Output = update.Status, update.Conclusion, update.Output
					f.json(w, f.runJSON(run))
					return
				}
			}
		}
		f.apiError(w, 404, "Not Found")
	case len(parts) == 3 && parts[0] == "issues" && parts[2] == "comments":
		if r.Method == "POST" {
			var input struct{ Body string }
			must(f.t, json.NewDecoder(r.Body).Decode(&input))
			c := &gh.IssueComment{ID: new(f.id()), Body: &input.Body, User: &gh.User{Login: new("queue-app[bot]"), Type: new("Bot")}, CreatedAt: &gh.Timestamp{Time: f.clock}, UpdatedAt: &gh.Timestamp{Time: f.clock}}
			f.comments[number] = append(f.comments[number], c)
			w.WriteHeader(201)
			f.json(w, f.commentJSON(c))
		} else {
			list := []map[string]any{}
			since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
			for _, c := range f.comments[number] {
				o := f.commentJSON(c)
				updated, _ := time.Parse(time.RFC3339, o["updated_at"].(string))
				if since.IsZero() || updated.After(since) {
					list = append(list, o)
				}
			}
			f.json(w, paginate(f, r, w, list))
		}
	case strings.HasPrefix(path, "git/refs/") && r.Method == "DELETE":
		ref := strings.TrimPrefix(path, "git/refs/heads/")
		if !f.refs[ref] {
			f.apiError(w, 422, "Reference does not exist")
			return
		}
		delete(f.refs, ref)
		w.WriteHeader(204)
	case path == "contents/.openmerge.yml":
		f.json(w, f.object("content"))
	default:
		f.t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.String())
		f.apiError(w, 404, "Not Found")
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
	return append([]T{}, list[start:end]...)
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
		e.Observations = append(e.Observations, forge.PullRequest{Number: n, HeadSHA: sha, Base: "main", Author: "contributor", HeadInBaseRepo: n == 2, Approvals: []forge.Approval{{User: user, SHA: sha, CanWrite: n == 1}}})
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
	e.SetCanWrite = func(user string, write bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if write {
			f.permissions[user] = "write"
		} else {
			f.permissions[user] = "read"
		}
	}
	e.AddForeignComment = func(pr int, body string) {
		f.comments[pr] = append(f.comments[pr], &gh.IssueComment{ID: new(f.id()), Body: &body, User: &gh.User{Login: new("attacker"), Type: new("User")}, CreatedAt: &gh.Timestamp{Time: f.clock}, UpdatedAt: &gh.Timestamp{Time: f.clock}})
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
