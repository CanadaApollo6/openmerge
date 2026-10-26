package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestAdminAuthorityCompletionCLI(t *testing.T) {
	digest := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	var gotQuery url.Values
	var gotAuth string
	reply := func(http.ResponseWriter, store.AuthorityCompletionRequest) {}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/authority-completion" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		gotQuery, gotAuth = r.URL.Query(), r.Header.Get("Authorization")
		request := store.AuthorityCompletionRequest{Installation: gotQuery.Get("installation"), Repository: gotQuery.Get("repo"), Digest: gotQuery.Get("digest")}
		if raw := gotQuery.Get("targets"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &request.Targets); err != nil {
				t.Errorf("targets not JSON: %v", err)
			}
		}
		reply(w, request)
	}))
	defer server.Close()
	t.Setenv("OPENMERGE_API_URL", server.URL)
	t.Setenv("OPENMERGE_API_TOKEN", "cli-secret-token")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	completion := func(complete bool, reason string) func(http.ResponseWriter, store.AuthorityCompletionRequest) {
		return func(w http.ResponseWriter, request store.AuthorityCompletionRequest) {
			_ = json.NewEncoder(w).Encode(store.AuthorityCompletion{Request: request, Complete: complete, Queues: []store.AuthorityQueueCompletion{{Installation: request.Installation, ID: 3, Base: "main", Digest: digest, ExpectedDigest: digest, Reason: reason}}})
		}
	}
	args := []string{"admin", "authority-completion", "--installation", "7", "--repo", "owner/repo", "--digest", digest}
	invoke := func(args []string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := run(args, &out, &stderr)
		return code, out.String(), stderr.String()
	}

	reply = completion(true, "complete")
	if code, out, stderr := invoke(args); code != 0 || !strings.Contains(out, `"complete":true`) || stderr != "" {
		t.Fatalf("complete: %d %q %q", code, out, stderr)
	}
	if gotQuery.Get("installation") != "7" || gotQuery.Get("repo") != "owner/repo" || gotQuery.Get("digest") != digest || gotQuery.Has("targets") || gotAuth != "Bearer cli-secret-token" {
		t.Fatalf("request: %v %q", gotQuery, gotAuth)
	}
	withTargets := append(append([]string{}, args...), "--targets", `{"release/v1":"`+other+`"}`)
	if code, _, stderr := invoke(withTargets); code != 0 || gotQuery.Get("targets") != `{"release/v1":"`+other+`"}` {
		t.Fatalf("targets: %d %q %v", code, stderr, gotQuery)
	}

	// HTTP 200 with complete=false is the normal incomplete answer: nonzero.
	reply = completion(false, "digest_mismatch")
	if code, out, stderr := invoke(args); code != 1 || !strings.Contains(out, "digest_mismatch") || !strings.Contains(stderr, "retain redundant protection") {
		t.Fatalf("incomplete: %d %q %q", code, out, stderr)
	}
	reply = completion(true, "digest_mismatch")
	if code, _, _ := invoke(args); code != 1 {
		t.Fatal("complete flag with incomplete row accepted")
	}
	// A complete flag without rows is never success.
	reply = func(w http.ResponseWriter, request store.AuthorityCompletionRequest) {
		_ = json.NewEncoder(w).Encode(store.AuthorityCompletion{Request: request, Complete: true, Queues: []store.AuthorityQueueCompletion{}})
	}
	if code, _, _ := invoke(args); code != 1 {
		t.Fatal("empty complete accepted")
	}
	// A completion for another intent (or a stale proxy answer) cannot satisfy this one.
	reply = func(w http.ResponseWriter, request store.AuthorityCompletionRequest) {
		request.Digest = other
		completion(true, "complete")(w, request)
	}
	if code, out, stderr := invoke(args); code != 1 || out != "" || !strings.Contains(stderr, "mismatched") {
		t.Fatalf("mismatched echo: %d %q %q", code, out, stderr)
	}
	reply = func(w http.ResponseWriter, _ store.AuthorityCompletionRequest) {
		_, _ = w.Write([]byte(`{"request":{},"complete":true,"queues":[],"extra":1}`))
	}
	if code, _, _ := invoke(args); code != 1 {
		t.Fatal("unknown response field accepted")
	}
	// Retry-class API failures exit nonzero with the sanitized server message.
	reply = func(w http.ResponseWriter, _ store.AuthorityCompletionRequest) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"version":%d,"error":"unavailable","message":"retry cli-secret-token"}`, 1)
	}
	if code, out, stderr := invoke(args); code != 1 || out != "" || strings.Contains(stderr, "cli-secret-token") {
		t.Fatalf("unavailable: %d %q %q", code, out, stderr)
	}
	reply = func(w http.ResponseWriter, _ store.AuthorityCompletionRequest) {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if code, _, _ := invoke(args); code != 4 {
		t.Fatal("unauthorized exit")
	}

	// Intent and origin are validated before any request.
	calls := 0
	reply = func(http.ResponseWriter, store.AuthorityCompletionRequest) { calls++ }
	for _, bad := range [][]string{
		{"admin", "authority-completion", "--repo", "owner/repo", "--digest", digest},
		{"admin", "authority-completion", "--installation", "7", "--digest", digest},
		{"admin", "authority-completion", "--installation", "7", "--repo", "owner/repo"},
		{"admin", "authority-completion", "--installation", "7", "--repo", "owner/repo", "--digest", "short"},
		{"admin", "authority-completion", "--installation", "7", "--repo", "owner/repo", "--targets", "{"},
		{"admin", "authority-completion", "--installation", "7", "--repo", "owner/repo", "--targets", `{"main":"short"}`},
		append(append([]string{}, args...), "extra"),
		append(append([]string{}, args...), "--api", "http://user:pw@127.0.0.1:1"),
	} {
		if code, _, _ := invoke(bad); code != 2 {
			t.Fatalf("%v: exit %d", bad, code)
		}
	}
	if calls != 0 {
		t.Fatal("invalid intent reached the API")
	}
	// Transport failures never echo the request URL or token.
	if code, _, stderr := invoke(append(append([]string{}, args...), "--api", "http://127.0.0.1:1")); code != 1 || strings.Contains(stderr, "127.0.0.1") || strings.Contains(stderr, "cli-secret-token") {
		t.Fatalf("transport: %d %q", code, stderr)
	}
}

func TestAdminDowngradeCheckCLI(t *testing.T) {
	for _, args := range [][]string{{"admin", "downgrade-check"}, {"admin", "downgrade-check", "--database-url", "postgres://x/y", "extra"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 2 || out.Len() != 0 {
			t.Fatalf("%v: %d %s", args, code, stderr.String())
		}
	}
	// An unreachable database decides nothing and never reports a refusal or the URL.
	var out, stderr bytes.Buffer
	code := run([]string{"admin", "downgrade-check", "--database-url", "postgres://operator:downgrade-secret@127.0.0.1:1/openmerge?connect_timeout=2"}, &out, &stderr)
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "unavailable") || strings.Contains(stderr.String(), "refused") || strings.Contains(stderr.String(), "downgrade-secret") {
		t.Fatalf("unreachable: %d %q %q", code, out.String(), stderr.String())
	}

	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	schema := testSchemaName(t, "downgrade_cli_")
	if _, err = conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	}()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	s, err := store.Open(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q, err := s.EnsureQueue(t.Context(), store.QueueKey{Installation: "1", Repository: "local/fixture", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int, wantText string) {
		t.Helper()
		var out, stderr bytes.Buffer
		if code := run([]string{"admin", "downgrade-check", "--database-url", u.String()}, &out, &stderr); code != want || !strings.Contains(out.String()+stderr.String(), wantText) {
			t.Fatalf("downgrade-check: %d %q %q", code, out.String(), stderr.String())
		}
	}
	check(0, "boundary absent")
	pin, err := os.ReadFile("../../internal/queue/testdata/wire/v1/event_policy_authority_applied.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := queue.DecodeEvent(pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(t.Context(), q, []queue.Event{event}, ""); err != nil {
		t.Fatal(err)
	}
	check(1, "authority downgrade refused")
}
