package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestChangeBriefCLI(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	id, _ := queue.PRChangeID("7", "owner/repo", "release/v1", "", 1)
	v, _ := brief.Project(brief.Fact{}, "agent")
	v.ChangeID = string(id)
	body, _ := json.Marshal(v)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.EscapedPath() != "/v1/queues/owner/repo/release%2Fv1/changes/"+string(id)+"/brief" || r.URL.RawQuery != "audience=agent" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for _, asJSON := range []bool{false, true} {
		args := []string{"change", "brief", string(id), "--repo", "owner/repo", "--base", "release/v1", "--audience", "agent", "--api", server.URL}
		if asJSON {
			args = append(args, "--json")
		}
		var out, errout bytes.Buffer
		if code := run(args, &out, &errout); code != 0 {
			t.Fatalf("exit %d: %s", code, errout.String())
		}
		if asJSON {
			if !bytes.Equal(out.Bytes(), body) {
				t.Fatal("JSON bytes changed")
			}
		} else {
			var got brief.View
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ChangeID != string(id) {
				t.Fatal("agent did not emit structured JSON")
			}
		}
	}
}
func TestChangeBriefExitCodes(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	id, _ := queue.PRChangeID("7", "owner/repo", "main", "", 1)
	for _, tc := range []struct {
		http, exit int
		name       string
	}{{404, 3, "not-found"}, {401, 4, "unauthorized"}, {403, 4, "forbidden"}, {409, 5, "ambiguous-queue"}, {400, 2, "invalid-request"}, {503, 1, "unavailable"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.http)
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "error": tc.name, "message": "failure"})
		}))
		var out, errout bytes.Buffer
		code := runChange([]string{"brief", string(id), "--repo", "owner/repo", "--api", server.URL}, &out, &errout)
		server.Close()
		if code != tc.exit {
			t.Fatalf("HTTP %d: exit %d: %s", tc.http, code, errout.String())
		}
	}
	for _, args := range [][]string{{}, {"brief", "bad"}, {"brief", string(id), "--repo", "owner/repo", "--audience", "bad"}, {"brief", string(id), "--repo", "../repo"}} {
		var out, errout bytes.Buffer
		if code := runChange(args, &out, &errout); code != 2 || strings.TrimSpace(errout.String()) == "" {
			t.Fatal("invalid invocation accepted")
		}
	}
}

func TestChangeBriefRejectsRedirectAndUnsupportedBodies(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	id, _ := queue.PRChangeID("7", "owner/repo", "main", "", 1)
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	for _, body := range []string{"redirect", `{"version":2}`, strings.Repeat("x", brief.MaxViewBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body == "redirect" {
				http.Redirect(w, r, target.URL, http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		var out, errout bytes.Buffer
		code := runChange([]string{"brief", string(id), "--repo", "owner/repo", "--api", server.URL}, &out, &errout)
		server.Close()
		if code != 1 || out.Len() != 0 || redirected {
			t.Fatalf("unsafe response accepted: %d %s", code, errout.String())
		}
	}
}
