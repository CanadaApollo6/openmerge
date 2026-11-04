package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The helper runs the production command in another process with actual stdio.
func TestMCPProcess(_ *testing.T) {
	if os.Getenv("OPENMERGE_MCP_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(os.Args[i+1:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

func mcpTestClient(t *testing.T, api string, extra ...string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	args := append([]string{"-test.run=^TestMCPProcess$", "--", "mcp", "--api", api}, extra...)
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.Env = append(os.Environ(), "OPENMERGE_MCP_TEST_PROCESS=1")
	var stderr, wire bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "scripted-agent", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: io.NopCloser(io.TeeReader(stdout, &wire)), Writer: stdin}, nil)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("initialize: %v; stderr: %s", err, &stderr)
	}
	t.Cleanup(func() {
		_ = session.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("MCP exit: %v; stderr: %s", err, &stderr)
		}
		if stderr.Len() != 0 {
			t.Errorf("unexpected stderr: %s", &stderr)
		}
		scanner := bufio.NewScanner(strings.NewReader(wire.String()))
		scanner.Buffer(make([]byte, 4096), 4*mcpMaxResponse)
		for scanner.Scan() {
			var frame map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &frame) != nil || string(frame["jsonrpc"]) != `"2.0"` {
				t.Errorf("non-protocol stdout: %.100s", scanner.Bytes())
			}
		}
		if err := scanner.Err(); err != nil {
			t.Error(err)
		}
		if token := os.Getenv("OPENMERGE_API_TOKEN"); token != "" && (strings.Contains(wire.String(), token) || strings.Contains(stderr.String(), token)) {
			t.Error("token leaked to stdout or stderr")
		}
	})
	return session
}

func mcpCall(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mcpText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("missing text content")
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("expected text content")
	}
	return content.Text
}

func mcpTestToken(t *testing.T) {
	t.Helper()
	t.Setenv("OPENMERGE_API_TOKEN", "mcp-private-test-token")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
}

type mcpJournalSource []store.Journal

func (s mcpJournalSource) Journals(context.Context, []store.QueueKey) ([]store.Journal, error) {
	return s, nil
}

func TestMCPRegistryAndRealHandler(t *testing.T) {
	mcpTestToken(t)
	key := store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "release/v1"}
	api := httptest.NewServer(status.Handler(mcpJournalSource{{Key: key, Seed: queue.State{BaseBranch: key.BaseBranch}}}, []store.QueueKey{key}, os.Getenv("OPENMERGE_API_TOKEN"), nil))
	defer api.Close()
	client := mcpTestClient(t, api.URL)
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	for _, read := range status.Reads() {
		if read.Name == "live" {
			continue
		}
		tool := tools[read.Name]
		if tool == nil {
			t.Fatalf("missing tool %s", read.Name)
		}
		delete(tools, read.Name)
		if tool.Description != read.Description || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool contract: %+v", tool)
		}
		schema := tool.InputSchema.(map[string]any)
		properties := schema["properties"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false || len(properties) != len(read.Parameters) {
			t.Fatalf("schema: %+v", schema)
		}
		required := []any{}
		if values, ok := schema["required"].([]any); ok {
			required = values
		}
		for _, p := range read.Parameters {
			property := properties[p.Name].(map[string]any)
			if property["type"] != p.Type || property["description"] != p.Description || slices.Contains(required, any(p.Name)) != p.Required {
				t.Errorf("%s parameter %s: %+v", read.Name, p.Name, schema)
			}
		}
	}
	if len(tools) != 0 {
		t.Fatalf("unregistered tools: %v", tools)
	}
	result := mcpCall(t, client, "queue", map[string]any{"owner": "owner", "repo": "repo", "base": "release/v1"})
	var diagnostics bytes.Buffer
	want, code := readStatusAPI(api.URL, "/v1/queues/owner/repo/release%2Fv1", "", &diagnostics)
	if code != 0 || result.IsError || mcpText(t, result) != string(want) {
		t.Fatalf("body changed or failed: %+v; %s", result, &diagnostics)
	}
	var expected any
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.StructuredContent, expected) {
		t.Fatal("structured content differs from API")
	}
}

func TestMCPEscaping(t *testing.T) {
	mcpTestToken(t)
	requests := make(chan *http.Request, 32)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(context.Background())
		_, _ = io.WriteString(w, "{\"version\":1}\n")
	}))
	defer api.Close()
	client := mcpTestClient(t, api.URL)
	for _, attack := range []struct{ value, escaped string }{
		{"release/v1", "release%2Fv1"}, {"../", "..%2F"}, {"%2f", "%252f"}, {"?", "%3F"}, {"#", "%23"},
	} {
		for _, parameter := range []string{"owner", "repo", "base", "id"} {
			args := map[string]any{"owner": "owner", "repo": "repo", "base": "main", "id": "change"}
			args[parameter] = attack.value
			result := mcpCall(t, client, "change", args)
			if result.IsError {
				t.Fatal(mcpText(t, result))
			}
			request := <-requests
			parts := map[string]string{"owner": "owner", "repo": "repo", "base": "main", "id": "change"}
			parts[parameter] = attack.escaped
			want := fmt.Sprintf("/v1/queues/%s/%s/%s/changes/%s", parts["owner"], parts["repo"], parts["base"], parts["id"])
			if request.Method != "GET" || request.URL.EscapedPath() != want || request.URL.RawQuery != "" || request.URL.Fragment != "" {
				t.Fatalf("parameter %s=%q escaped to %s, want %s", parameter, attack.value, request.URL, want)
			}
		}
	}
	result := mcpCall(t, client, "brief", map[string]any{"owner": "owner", "repo": "repo", "base": "main", "id": "id", "audience": "../%2f?#", "revision": 2})
	request := <-requests
	if result.IsError || request.URL.Query().Get("audience") != "../%2f?#" || request.URL.Query().Get("revision") != "2" || request.URL.EscapedPath() != "/v1/queues/owner/repo/main/changes/id/brief" {
		t.Fatalf("query mapping: %+v %s", result, request.URL)
	}
	for _, args := range []map[string]any{
		{}, {"owner": "..", "repo": "repo", "base": "main"}, {"owner": ".", "repo": "repo", "base": "main"},
		{"owner": "o", "repo": "r", "base": "main", "path": "/admin"}, {"owner": "o", "repo": "r", "base": nil},
	} {
		if !mcpCall(t, client, "queue", args).IsError {
			t.Fatal("invalid input accepted")
		}
	}
	if !mcpCall(t, client, "pr", map[string]any{"owner": "o", "repo": "r", "base": "main", "n": "../"}).IsError {
		t.Fatal("wrong integer type accepted")
	}
	select {
	case <-requests:
		t.Fatal("invalid arguments reached HTTP")
	default:
	}
}

func TestMCPErrorsComputingAndSize(t *testing.T) {
	mcpTestToken(t)
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		retry   string
		want    string
		isError bool
	}{
		{"unauthorized", 401, `{"version":1,"error":"unauthorized","message":"mcp-private-test-token"}`, "", "unauthorized: [REDACTED] (HTTP 401)", true},
		{"forbidden", 403, `{"version":1,"error":"forbidden","message":"denied"}`, "", "forbidden: denied (HTTP 403)", true},
		{"missing", 404, `{"version":1,"error":"not-found","message":"no queue"}`, "", "not-found: no queue (HTTP 404)", true},
		{"ambiguous", 409, `{"version":1,"error":"ambiguous-queue","message":"select partition"}`, "", "ambiguous-queue: select partition (HTTP 409)", true},
		{"computing", 202, "{\"version\":1,\"status\":\"computing\"}\n", "7", "computing", false},
		{"at-limit", 200, `{"padding":"` + strings.Repeat("x", mcpMaxResponse-14) + `"}`, "", "padding", false},
		{"over-limit", 200, `{"padding":"` + strings.Repeat("x", mcpMaxResponse-13) + `"}`, "", "MCP body limit is 65536 bytes", true},
		{"invalid-json", 200, "not JSON", "", "invalid, oversized or unsupported", true},
		{"secret-body", 200, `{"echo":"mcp-private-test-token"}`, "", "invalid, oversized or unsupported", true},
		{"malformed-error", 500, "mcp-private-test-token", "", "status API returned HTTP 500", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer mcp-private-test-token" {
					t.Error("missing auth header")
				}
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer api.Close()
			result := mcpCall(t, mcpTestClient(t, api.URL), "queues", nil)
			text := mcpText(t, result)
			if result.IsError != tc.isError || !strings.Contains(text, tc.want) {
				t.Fatalf("result error=%t text=%.200s", result.IsError, text)
			}
			if !result.IsError && text != tc.body {
				t.Fatal("successful body changed")
			}
			if tc.retry != "" && (len(result.Content) != 2 || !strings.Contains(result.Content[1].(*mcp.TextContent).Text, "Retry-After: 7")) {
				t.Fatal("computing response lost retry time")
			}
			if tc.name == "over-limit" && !strings.Contains(text, "repository_queues") {
				t.Fatal("missing narrower read advice")
			}
		})
	}
}

func TestMCPTokenConfiguration(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	var out, stderr bytes.Buffer
	if code := run([]string{"mcp"}, &out, &stderr); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "requires OPENMERGE_API_TOKEN") {
		t.Fatalf("missing token: %d %s %s", code, &out, &stderr)
	}
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("file-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer file-secret" {
			t.Error("token file ignored")
		}
		_, _ = io.WriteString(w, `{"version":1}`)
	}))
	defer api.Close()
	if mcpCall(t, mcpTestClient(t, api.URL, "--api-token-file", file), "queues", nil).IsError {
		t.Fatal("token file read failed")
	}
}

func TestMCPRefusesRedirects(t *testing.T) {
	mcpTestToken(t)
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer api.Close()
	result := mcpCall(t, mcpTestClient(t, api.URL), "queues", nil)
	if !result.IsError || !strings.Contains(mcpText(t, result), "HTTP 302") || followed.Load() != 0 {
		t.Fatal("redirect followed or not reported")
	}
}

// Run with OPENMERGE_MCP_BINARY pointing at a freshly built production binary.
func TestMCPBinarySurface(t *testing.T) {
	binary := os.Getenv("OPENMERGE_MCP_BINARY")
	if binary == "" {
		t.Skip("set OPENMERGE_MCP_BINARY for the production binary stdio proof")
	}
	mcpTestToken(t)
	key := store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "release/v1"}
	api := httptest.NewServer(status.Handler(mcpJournalSource{{Key: key, Seed: queue.State{BaseBranch: key.BaseBranch}}}, []store.QueueKey{key}, os.Getenv("OPENMERGE_API_TOKEN"), nil))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "mcp", "--api", api.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("binary exit: %v", err)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr: %s", &stderr)
		}
	}()
	decoder := json.NewDecoder(stdout)
	send := func(message string) {
		t.Helper()
		if _, err := io.WriteString(stdin, message+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	receive := func(id int) json.RawMessage {
		t.Helper()
		for {
			var frame struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      int             `json:"id"`
				Result  json.RawMessage `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if err := decoder.Decode(&frame); err != nil {
				t.Fatal(err)
			}
			if frame.JSONRPC != "2.0" || len(frame.Error) > 0 {
				t.Fatalf("invalid JSON-RPC response: %+v", frame)
			}
			if frame.ID == id {
				if bytes.Contains(frame.Result, []byte(os.Getenv("OPENMERGE_API_TOKEN"))) {
					t.Fatal("token leaked")
				}
				return frame.Result
			}
		}
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw-stdio-proof","version":"1"}}}`)
	_ = receive(1)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	listed := receive(2)
	t.Logf("tools/list result: %s", listed)
	var list struct {
		Tools []mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(listed, &list); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("tools/list names: %s", strings.Join(names, ", "))
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"queue","arguments":{"owner":"owner","repo":"repo","base":"release/v1"}}}`)
	called := receive(3)
	t.Logf("tools/call result: %s", called)
	var result mcp.CallToolResult
	if err := json.Unmarshal(called, &result); err != nil || result.IsError || !strings.Contains(mcpText(t, &result), `"base":"release/v1"`) {
		t.Fatalf("real binary call failed: %s", called)
	}
}
