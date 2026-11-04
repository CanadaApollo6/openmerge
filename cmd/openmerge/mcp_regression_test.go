package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPEscapedTokenReflection(t *testing.T) {
	mcpTestToken(t)
	var escaped strings.Builder
	for _, r := range os.Getenv("OPENMERGE_API_TOKEN") {
		_, _ = fmt.Fprintf(&escaped, `\u%04x`, r)
	}
	for name, body := range map[string]string{
		"value":         `{"nested":[{"echo":"prefix ` + escaped.String() + ` suffix"}]}`,
		"key":           `{"nested":[{"prefix ` + escaped.String() + ` suffix":1}]}`,
		"duplicate-key": `{"echo":"` + escaped.String() + `","echo":"safe"}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer api.Close()
			result := mcpCall(t, mcpTestClient(t, api.URL), "queues", nil)
			if !result.IsError || !strings.HasPrefix(mcpText(t, result), "invalid, oversized or unsupported status API response") || result.StructuredContent != nil {
				t.Fatalf("escaped token accepted: error=%t, structured=%t", result.IsError, result.StructuredContent != nil)
			}
		})
	}
}

func TestMCPAcceptedEscapesPreserveBytes(t *testing.T) {
	mcpTestToken(t)
	body := " {\"key\\u002f\": [\"a\\u002fb\", 1]}\n"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer api.Close()
	result := mcpCall(t, mcpTestClient(t, api.URL), "queues", nil)
	if result.IsError || mcpText(t, result) != body {
		t.Fatalf("accepted body changed: %+v", result)
	}
	structured := result.StructuredContent.(map[string]any)
	if structured["key/"].([]any)[0] != "a/b" {
		t.Fatalf("structured content differs: %v", structured)
	}
}

func TestMCPRetryAfterErrors(t *testing.T) {
	mcpTestToken(t)
	for _, code := range []int{202, 429, 503} {
		for _, retry := range []string{"17", "0", "86400", "", "bogus", "-1", "+17", "1.5", "86401", strings.Repeat("9", 80), "Wed, 21 Oct 2015 07:28:00 GMT", "mcp-private-test-token"} {
			t.Run(fmt.Sprintf("%d/%s", code, retry), func(t *testing.T) {
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Retry-After", retry)
					w.WriteHeader(code)
					_, _ = io.WriteString(w, `{"version":1,"error":"unavailable","message":"try later"}`)
				}))
				defer api.Close()
				result := mcpCall(t, mcpTestClient(t, api.URL), "queues", nil)
				if result.IsError != (code != 202) || code != 202 && !strings.Contains(mcpText(t, result), fmt.Sprintf("HTTP %d", code)) {
					t.Fatalf("lost status: %+v", result)
				}
				if retry == "17" || retry == "0" || retry == "86400" {
					want := "Retry-After: " + retry + "; wait, then repeat this read."
					if len(result.Content) != 2 || result.Content[1].(*mcp.TextContent).Text != want {
						t.Fatalf("lost numeric retry interval: %+v", result)
					}
				} else if len(result.Content) != 1 {
					t.Fatalf("invalid retry interval accepted: %+v", result)
				}
			})
		}
	}
}

func TestMCPLargeRealQueue(t *testing.T) {
	mcpTestToken(t)
	key := store.QueueKey{Installation: "7", Repository: "o/r", BaseBranch: "main"}
	journal := store.Journal{Key: key, Seed: queue.State{BaseBranch: "main", BaseSHA: "base", Fence: "fence"}}
	at := time.Unix(1700000000, 0)
	for pr := 1; pr <= 100; pr++ {
		for _, event := range []queue.Event{
			queue.PRObserved{PR: pr, HeadSHA: strings.Repeat("a", 40), Eligible: true, Trusted: true},
			queue.EnqueueRequested{PR: pr, At: at},
		} {
			_, payload, err := queue.EncodeEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			journal.Events = append(journal.Events, store.JournalEvent{ID: int64(len(journal.Events) + 1), RecordedAt: at, Payload: payload, Event: event})
		}
	}
	api := httptest.NewServer(status.Handler(mcpJournalSource{journal}, []store.QueueKey{key}, os.Getenv("OPENMERGE_API_TOKEN"), nil))
	defer api.Close()
	client := mcpTestClient(t, api.URL)
	for _, tc := range []struct {
		name string
		args map[string]any
		path string
	}{
		{"queues", nil, "/v1/queues"},
		{"repository_queues", map[string]any{"owner": "o", "repo": "r"}, "/v1/queues/o/r"},
		{"queue", map[string]any{"owner": "o", "repo": "r", "base": "main"}, "/v1/queues/o/r/main"},
		{"pr", map[string]any{"owner": "o", "repo": "r", "base": "main", "n": 1, "events": 1}, "/v1/queues/o/r/main/prs/1?events=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			body, code := readStatusAPI(api.URL, tc.path, "", &diagnostics)
			if code != 0 || len(body) <= 64<<10 || len(body) >= 256<<10 {
				t.Fatalf("invalid large-queue fixture: code=%d bytes=%d %s", code, len(body), &diagnostics)
			}
			result := mcpCall(t, client, tc.name, tc.args)
			if result.IsError || mcpText(t, result) != string(body) {
				t.Fatalf("ordinary queue read refused or changed: error=%t, bytes=%d", result.IsError, len(body))
			}
			t.Logf("%s accepted %d unchanged bytes", tc.name, len(body))
		})
	}
}

func TestMCPSizeAdvicePerRead(t *testing.T) {
	mcpTestToken(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"padding":"`+strings.Repeat("x", 256<<10)+`"}`)
	}))
	defer api.Close()
	client := mcpTestClient(t, api.URL)
	for _, read := range status.Reads() {
		if read.Name == "live" {
			continue
		}
		t.Run(read.Name, func(t *testing.T) {
			args := map[string]any{}
			for _, p := range read.Parameters {
				if p.Required {
					args[p.Name] = "value"
					if p.Type == "integer" || p.Type == "number" {
						args[p.Name] = 1
					}
				}
			}
			check := func(want string) {
				t.Helper()
				result := mcpCall(t, client, read.Name, args)
				text := mcpText(t, result)
				if !result.IsError || !strings.Contains(text, "MCP body limit is 262144 bytes") || !strings.Contains(text, want) || !strings.Contains(text, "openmerge queue status --json") || result.StructuredContent != nil {
					t.Fatalf("incorrect oversize advice for %s: %.500s", read.Name, text)
				}
			}
			switch read.Name {
			case "decisions":
				check("limit=1")
				args["limit"] = 2
				check("limit=1")
				args["limit"] = 1
			case "pr":
				check("events=1")
				args["events"] = 2
				check("events=1")
				args["events"] = 1
			}
			check("no narrower read exists")
		})
	}
}

func TestMCPFutureDetailSizeAdvice(t *testing.T) {
	read := status.Read{Name: "future_report", Parameters: []status.Parameter{
		{Name: "detail", In: "query", Type: "string", Description: "One of summary or full. Defaults to summary."},
	}}
	for _, tc := range []struct {
		arguments string
		want      string
	}{
		{`{"detail":"full"}`, "retry with detail=summary"},
		{`{"detail":"summary"}`, "no narrower read exists"},
		{`{}`, "no narrower read exists"},
	} {
		if got := mcpNarrowerRead(read, []byte(tc.arguments)); !strings.Contains(got, tc.want) {
			t.Errorf("arguments %s: %s", tc.arguments, got)
		}
	}
}
