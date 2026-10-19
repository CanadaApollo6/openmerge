package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
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

func TestChangeBriefHelpBeforeID(t *testing.T) {
	for _, args := range [][]string{{"brief", "--help"}, {"brief", "-h"}} {
		var out, errout bytes.Buffer
		if code := runChange(args, &out, &errout); code != 0 {
			t.Fatalf("help exit=%d: %s", code, errout.String())
		}
	}
}

func TestChangeShowCLI(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	id, _ := queue.PRChangeID("7", "owner/repo", "release/v1", "", 1)
	view, _ := brief.Project(brief.Fact{}, "reviewer")
	view.ChangeID = string(id)
	response := status.ChangeResponse{Version: 1, PR: &status.Entry{PR: 1, Status: "queued", Change: &status.ChangeReport{ID: string(id), Revision: 1, Accountability: status.AccountabilityView{Status: brief.NotRecorded}}}, Brief: view}
	body, _ := json.Marshal(response)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.EscapedPath() != "/v1/queues/owner/repo/release%2Fv1/changes/"+string(id) || r.URL.RawQuery != "revision=1" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected show request: %s", r.URL)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	for _, asJSON := range []bool{true, false} {
		args := []string{"show", string(id), "--repo", "owner/repo", "--base", "release/v1", "--revision", "1", "--api", server.URL}
		if asJSON {
			args = append(args, "--json")
		}
		var out, errout bytes.Buffer
		if code := runChange(args, &out, &errout); code != 0 {
			t.Fatalf("exit %d: %s", code, &errout)
		}
		if asJSON && !bytes.Equal(out.Bytes(), body) {
			t.Fatal("JSON changed")
		}
		if !asJSON && !strings.Contains(out.String(), "Accountability: not recorded") {
			t.Fatal(out.String())
		}
	}
}

func TestChangeShowExitCodesAndReadOnlyCommands(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	id, _ := queue.PRChangeID("7", "owner/repo", "main", "", 1)
	for _, tc := range []struct{ http, exit int }{{404, 3}, {401, 4}, {403, 4}, {409, 5}, {400, 2}, {503, 1}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.http)
			name := "failure"
			if tc.http == 400 {
				name = "invalid-request"
			}
			if tc.http == 409 {
				name = "ambiguous-queue"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "error": name, "message": "failed"})
		}))
		var out, errout bytes.Buffer
		code := runChange([]string{"show", string(id), "--repo", "owner/repo", "--api", server.URL}, &out, &errout)
		server.Close()
		if code != tc.exit {
			t.Fatal(tc.http, code, &errout)
		}
	}
	for _, command := range []string{"submit", "approve"} {
		var out, errout bytes.Buffer
		if runChange([]string{command, string(id)}, &out, &errout) != 2 {
			t.Fatal("write command accepted")
		}
	}
}

func TestChangeShowCLIGolden(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	body, err := os.ReadFile("../../internal/status/testdata/change-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var response status.ChangeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("write request: %s", r.Method)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	var out, errout bytes.Buffer
	code := runChange([]string{"show", response.PR.Change.ID, "--repo", "owner/repo", "--api", server.URL}, &out, &errout)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &errout)
	}
	path := "testdata/change-show.txt"
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, out.Bytes()) {
		t.Fatalf("CLI golden mismatch: %v", err)
	}
}

func TestChangeShowLandedCLI(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	body, err := os.ReadFile("../../internal/status/testdata/change-show.json")
	if err != nil {
		t.Fatal(err)
	}
	var response status.ChangeResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	response.PR.Status = "merged"
	response.PR.Landing = &status.LandingView{Outcome: "landed", SHA: strings.Repeat("a", 40), AttemptID: "attempt-1", EventID: 42}
	body, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	var out, errout bytes.Buffer
	code := runChange([]string{"show", response.PR.Change.ID, "--repo", "owner/repo", "--api", server.URL}, &out, &errout)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, &errout)
	}
	for _, want := range []string{`Recorded landing: "landed"`, strings.Repeat("a", 40), `attempt "attempt-1"`, `event 42`, server.URL + response.PR.Change.DecisionsURL} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, &out)
		}
	}
}
