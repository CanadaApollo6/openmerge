package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/gitops"
	"github.com/CanadaApollo6/openmerge/internal/serve"
	"github.com/CanadaApollo6/openmerge/internal/store"
	gh "github.com/google/go-github/v93/github"
	"github.com/jackc/pgx/v5"
)

func serveDatabase(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("run make db and set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	must(t, err)
	schema := fmt.Sprintf("serve_%d", time.Now().UnixNano())
	_, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	must(t, err)
	t.Cleanup(func() {
		_, err := conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		must(t, err)
		must(t, conn.Close(context.Background()))
	})
	u, err := url.Parse(raw)
	must(t, err)
	params := u.Query()
	params.Set("search_path", schema)
	u.RawQuery = params.Encode()
	db, err := store.Open(t.Context(), u.String())
	must(t, err)
	t.Cleanup(db.Close)
	return db
}

func localGit(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	if dir != "" {
		args = append([]string{"--git-dir=" + dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestServeCommentToLanding(t *testing.T) {
	db := serveDatabase(t)
	f := newAPI(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	localGit(t, "", "", "init", "--bare", "--template=", remote)
	configuration := "version: 1\nqueues:\n  - name: default\n    merge_method: squash\n    merge_when:\n      - check-success = ci\n"
	configBlob := localGit(t, remote, configuration, "hash-object", "-w", "--stdin")
	tree := localGit(t, remote, "100644 blob "+configBlob+"\t.openmerge.yml\n", "mktree")
	base := localGit(t, remote, "base", "commit-tree", tree)
	localGit(t, remote, "", "update-ref", "refs/heads/main", base)
	heads := make(map[int]string)
	for _, n := range []int{1, 2} {
		blob := localGit(t, remote, fmt.Sprintf("PR %d", n), "hash-object", "-w", "--stdin")
		tree := localGit(t, remote, "100644 blob "+configBlob+"\t.openmerge.yml\n100644 blob "+blob+fmt.Sprintf("\tpr-%d.txt\n", n), "mktree")
		heads[n] = localGit(t, remote, fmt.Sprintf("PR %d\n\nSigned-off-by: Real <real@example.test>\nCo-authored-by: Carol <carol@example.test>\nOpenMerge-Batch: contributor", n), "commit-tree", tree, "-p", base)
		localGit(t, remote, "", "update-ref", HeadRef(n), heads[n])
		if n == 2 {
			localGit(t, remote, "", "update-ref", "refs/heads/pr-2", heads[n])
		}
		f.prs[n].Head.SHA = new(heads[n])
		f.reviews[n] = nil
		f.prs[n].Body = new("Description\nSigned-off-by: Forged <forged@example.test>\nCo-authored-by: Impostor <impostor@example.test>\nOpenMerge-Batch: body")
	}
	addComment := func(pr int, id int64, author string) {
		f.comments[pr] = append(f.comments[pr], &gh.IssueComment{ID: new(id), Body: new("@openmerge queue"), User: &gh.User{Login: &author, Type: new("User")}, CreatedAt: &gh.Timestamp{Time: epoch}, UpdatedAt: &gh.Timestamp{Time: epoch}})
	}
	addComment(1, 10, "writer") // fork has no exact-head approval
	addComment(2, 11, "reader")
	addComment(2, 12, "writer")
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v3/repos/acme/project" {
			f.json(w, map[string]any{"id": 50, "default_branch": "main"})
			return true
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v3/repos/acme/project/")
		if path == "branches/pr-2" {
			f.json(w, map[string]any{"name": "pr-2", "protected": false, "commit": map[string]any{"sha": heads[2]}})
			return true
		}
		if strings.HasPrefix(path, "contents/") {
			if path != "contents/.openmerge.yml" {
				f.apiError(w, 404, "Not Found")
				return true
			}
			ref := r.URL.Query().Get("ref")
			if len(ref) != 40 {
				t.Error("config was not pinned to a base SHA")
			}
			object := f.object("content")
			object["content"] = base64.StdEncoding.EncodeToString([]byte(configuration))
			f.json(w, object)
			return true
		}
		if strings.HasPrefix(path, "commits/") && strings.HasSuffix(path, "/check-runs") {
			sha := strings.Split(path, "/")[1]
			if f.runs[sha] == nil {
				f.runs[sha] = []*gh.CheckRun{{ID: new(int64(300)), Name: new("ci"), Status: new("completed"), Conclusion: new("success"), App: &gh.App{ID: new(int64(99))}}}
			}
		}
		if r.Method == "DELETE" && strings.HasPrefix(path, "git/refs/") {
			localGit(t, remote, "", "update-ref", "-d", "refs/"+strings.TrimPrefix(path, "git/refs/"))
			w.WriteHeader(204)
			return true
		}
		return false
	}
	committer, err := f.driver.Committer(t.Context())
	must(t, err)
	git, err := gitops.New(t.Context(), gitops.Config{WorkDir: t.TempDir(), Remote: func(context.Context, forge.RepoRef) (string, gitops.Auth, error) { return remote, gitops.Auth{}, nil }, HeadRef: HeadRef, PullRequest: f.driver.PullRequestMetadata, CommitAuthors: f.driver.CommitAuthors, Committer: committer})
	must(t, err)
	initial, err := git.EnsureFence(t.Context(), testRepo, "openmerge/fence/main")
	must(t, err)
	key := store.QueueKey{Installation: "7", Repository: testRepo.Path, BaseBranch: "main"}
	server, err := serve.New(t.Context(), db, []serve.Target{{Key: key, Driver: f.driver, Git: git, BotLogin: f.driver.BotLogin()}}, serve.Options{PollInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); must(t, <-done) })
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	deadline := time.Now().Add(15 * time.Second)
	for {
		state, _, err := db.Load(t.Context(), id)
		must(t, err)
		if state.BaseSHA != "" && state.BaseSHA != base && state.Active == nil && len(state.Entries) == 0 {
			f.mu.Lock()
			closed := f.prs[2].GetState() == "closed"
			f.mu.Unlock()
			if _, err := git.ReadRef(t.Context(), testRepo, "pr-2"); closed && err != nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve did not land: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	tip, err := git.ReadRef(t.Context(), testRepo, "main")
	must(t, err)
	if content := localGit(t, remote, "", "show", tip+":pr-2.txt"); content != "PR 2" {
		t.Fatal("wrong PR landed")
	}
	if identity := localGit(t, remote, "", "show", "-s", "--format=%an <%ae>|%cn <%ce>", tip); identity != "contributor <12+contributor@users.noreply.github.com>|queue-app[bot] <99+queue-app[bot]@users.noreply.github.com>" {
		t.Fatalf("landed identity=%s", identity)
	}
	message := localGit(t, remote, "", "show", "-s", "--format=%B", tip)
	batch := forge.ParseBatchMessage(message)
	if batch == "" {
		t.Fatalf("landed message has no authoritative batch: %s", message)
	}
	wantMessage := "Test change 2 (#2)\n\nDescription\n\nCo-authored-by: Fixture <fixture@localhost>\nSigned-off-by: Real <real@example.test>\nCo-authored-by: Carol <carol@example.test>\nOpenMerge-Batch: " + batch
	if message != wantMessage {
		t.Fatalf("landed message=%q, want %q", message, wantMessage)
	}
	if _, _, tested, err := git.ReadFence(t.Context(), testRepo, "openmerge/fence/main"); err != nil || tested != tip {
		t.Fatalf("fence tested=%s, tip=%s, error=%v", tested, tip, err)
	}
	current, err := git.EnsureFence(t.Context(), testRepo, "openmerge/fence/main")
	must(t, err)
	if current == initial {
		t.Fatal("landing did not advance fence")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prs[1].GetState() != "open" {
		t.Fatal("unapproved fork was closed")
	}
	blocked := false
	for _, run := range f.runs[heads[1]] {
		if strings.Contains(run.GetOutput().GetTitle(), "untrusted") {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("unapproved fork has no visible refusal")
	}
	replies := 0
	for _, c := range f.comments[2] {
		if c.GetUser().GetLogin() == "queue-app[bot]" && strings.Contains(c.GetBody(), "Only repository writers") {
			replies++
		}
	}
	if replies != 1 {
		t.Fatalf("unauthorized replies=%d", replies)
	}
}
