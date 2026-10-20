package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

const statusJSON = `{"version":1,"queue":{"installation":"7","repo":"owner/repo","base":"release/v1","lane":"","watermark":3,"base_sha":"base","paused":true,"pause_reason":"repository-rules","pause_detail":"fix fence","blocks":[{"reason":"repository-rules","detail":"fix fence"},{"reason":"action-poisoned","detail":"invalid action"}],"active_batch":null,"entries":[{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""}]},"pr":{"position":1,"pr":1,"head_sha":"0123456789012345","admitted_at":"2026-10-16T12:00:00Z","status":"paused","reason":"repository-rules","last_dequeue_reason":""},"why":{"summary":"PR #1: paused (repository-rules): fix fence","limit":20,"events":[{"id":3,"type":"rules_blocked","at":"2026-10-16T12:00:00Z","summary":"Repository rules block: fix fence"}]}}
`

func TestQueueStatusJSONIsVerbatimAndHumanIsCompact(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "test-secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/v1/queues/owner/repo/release%2Fv1/prs/1" || r.Header.Get("Authorization") != "Bearer test-secret" || r.Method != "GET" {
			t.Errorf("request=%s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, statusJSON)
	}))
	defer server.Close()
	args := []string{"queue", "status", "--api", server.URL, "--repo", "owner/repo", "--base", "release/v1", "--pr", "1"}
	var out, errOut bytes.Buffer
	if code := run(append(args, "--json"), &out, &errOut); code != 0 || out.String() != statusJSON || errOut.Len() > 0 {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errOut)
	}
	out.Reset()
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, &errOut)
	}
	for _, text := range []string{"REPO", "owner/repo", "release/v1", "#1", "012345678901", "paused", "repository-rules", "action-poisoned", "rules_blocked", "Repository rules block: fix fence"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("human output missing %q: %s", text, &out)
		}
	}
}

func TestQueueStatusPrintsBoundedSanitizedServerErrors(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "local-api-secret")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		status int
		code   string
		want   int
	}{
		{400, "invalid-request", 2},
		{401, "unauthorized", 4},
		{404, "not-found", 3},
		{409, "ambiguous-queue", 5},
		{413, "request-too-large", 2},
		{503, "unavailable", 1},
		{409, "unknown-error", 1},
	} {
		t.Run(tc.code, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"version": 1, "error": tc.code, "message": "queue diagnostic: local-api-secret ghp_hidden123 Bearer hidden-token\n\x1b\u202e " + strings.Repeat("x", 2000)})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			code := runQueue([]string{"status", "--api", server.URL, "--json"}, &out, &errOut)
			if code != tc.want || out.Len() != 0 || !strings.Contains(errOut.String(), tc.code+": queue diagnostic:") || !strings.Contains(errOut.String(), "[REDACTED]") || errOut.Len() > 1400 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &errOut)
			}
			for _, unsafe := range []string{"local-api-secret", "hidden123", "hidden-token", "\x1b", "\u202e"} {
				if strings.Contains(errOut.String(), unsafe) {
					t.Fatalf("unsafe server diagnostic %q", unsafe)
				}
			}
		})
	}
	for _, body := range []string{`<html>private raw body</html>`, `{"version":1,"error":"unavailable","message":"` + strings.Repeat("x", 8192) + `"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, body)
		}))
		var out, errOut bytes.Buffer
		code := runQueue([]string{"status", "--api", server.URL}, &out, &errOut)
		server.Close()
		if code != 1 || out.Len() != 0 || errOut.String() != "status API returned HTTP 503\n" {
			t.Fatalf("invalid/oversized error: %d %s %s", code, &out, &errOut)
		}
	}
}

func TestQueueStatusExitsAndNeverFollowsRedirects(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{200, `{"version":1,"queues":[]}`, 0},
		{200, `{"version":99,"queues":[]}`, 1},
		{200, `{"version":1}`, 1},
		{200, `not JSON`, 1},
		{404, ``, 3}, {401, ``, 4}, {403, ``, 4}, {503, ``, 1}, {302, ``, 1},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			if code := runQueue([]string{"status", "--api", server.URL, "--json"}, &out, &errOut); code != tc.want || calls != 1 {
				t.Fatalf("code=%d want=%d calls=%d err=%s", code, tc.want, calls, &errOut)
			}
			if tc.want != 0 && out.Len() != 0 {
				t.Fatal("error emitted success JSON")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	if code := runQueue([]string{"status", "--api", server.URL}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("transport failure=%d", code)
	}
}

func TestQueueStatusUsageAndOutputFailures(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	for _, args := range [][]string{nil, {"write"}, {"status", "extra"}, {"status", "--pr", "1"}, {"status", "--base", "main"}, {"status", "--repo", "bad"}, {"status", "--repo", "owner/repo", "--pr", "0"}, {"status", "--api", "file:///tmp/status"}, {"status", "--api", "http://user:secret@localhost"}, {"status", "--api", "http://localhost/path"}, {"status", "--database-url", "postgres://local"}} {
		if code := runQueue(args, io.Discard, io.Discard); code != 2 {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
	if code := runQueue([]string{"status", "--help"}, io.Discard, io.Discard); code != 0 {
		t.Fatal("help failed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, statusJSON) }))
	defer server.Close()
	for _, extra := range [][]string{nil, {"--json"}} {
		if code := runQueue(append([]string{"status", "--api", server.URL, "--repo", "owner/repo", "--pr", "1"}, extra...), brokenWriter{}, io.Discard); code != 1 {
			t.Fatalf("output failure=%d", code)
		}
	}
}

func TestStatusPrintsProblemsPerRulesSource(t *testing.T) {
	var response status.Response
	if err := json.Unmarshal([]byte(statusJSON), &response); err != nil {
		t.Fatal(err)
	}
	response.Queue.RulesSources = []status.RulesSource{
		{Source: "rulesets", Problems: []string{"repair fence rules"}, Revision: 1},
		{Source: "classic_protection", Problems: []string{"remove required reviews"}, Revision: 2},
		{Source: "permissions", Problems: []string{}, Revision: 2},
	}
	response.Queue.Blocks = append(response.Queue.Blocks, status.Block{Source: "rulesets", Reason: "repository-rules", Detail: "repair fence rules", Revision: 1})
	var out bytes.Buffer
	if err := printStatus(&out, response); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "repair fence rules") != 1 {
		t.Fatal("rules source printed twice")
	}
	for _, text := range []string{"rules (rulesets): repair fence rules", "rules (classic_protection): remove required reviews"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("source problems missing from status: %s", &out)
		}
	}
	if strings.Contains(out.String(), "rules (permissions)") {
		t.Fatal("cleared source printed as a block")
	}
}

func TestStatusTableInactivePositionAndPendingRange(t *testing.T) {
	for _, state := range []string{"merged", "dequeued", "not-queued", "queued"} {
		t.Run(state, func(t *testing.T) {
			position, want := 0, "-"
			if state == "queued" {
				position, want = 2, "2"
			}
			until := time.Date(2026, 10, 16, 12, 1, 0, 0, time.UTC)
			response := status.Response{Queue: &status.Queue{Repository: "owner/repo", Base: "main"}, PR: &status.Entry{PR: 1, Position: position, Status: state, HeadSHA: "head"}, Why: &status.Why{Summary: "PR #1: " + state, Events: []status.Event{{ID: 6, EndID: 15, At: until.Add(-time.Minute), Until: &until, Summary: "CI still pending", Count: 5}}}}
			var out bytes.Buffer
			if err := printStatus(&out, response); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(out.String(), "\n")
			fields := strings.Fields(lines[1])
			if len(fields) < 6 || fields[2] != want {
				t.Fatalf("bad position: %s", &out)
			}
			for _, text := range []string{"CI still pending (5 observations)", "2026-10-16T12:00:00Z – 2026-10-16T12:01:00Z", "6–15"} {
				if !strings.Contains(out.String(), text) {
					t.Fatalf("missing %q: %s", text, &out)
				}
			}
		})
	}
}

func TestQueueStatusRepositoryListsEveryBase(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/queues/owner/repo" {
			t.Errorf("requested %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"version":1,"queues":[{"repo":"owner/repo","base":"main","state":"active","entries":[]},{"repo":"owner/repo","base":"release/v1","state":"dormant","entries":[]}]}`)
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	if code := runQueue([]string{"status", "--api", server.URL, "--repo", "owner/repo"}, &out, &stderr); code != 0 {
		t.Fatalf("code=%d: %s", code, &stderr)
	}
	for _, text := range []string{"main", "release/v1", "active", "dormant", "STATE"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %s: %s", text, &out)
		}
	}
}

func TestStatusPrintsEveryIndependentBlock(t *testing.T) {
	response := status.Response{Queue: &status.Queue{Repository: "owner/repo", Base: "main", Blocks: []status.Block{
		{Source: "config", Reason: "invalid-config", Detail: "configuration unavailable", Revision: 3},
		{Source: "operator", Reason: "legacy-hold", Detail: "legacy operator detail", Revision: 1},
		{Source: "action-poisoned", Reason: "action-poisoned", Detail: "invalid action", Revision: 2},
	}}}
	var out bytes.Buffer
	if err := printStatus(&out, response); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"block (config): configuration unavailable", "block (operator): legacy operator detail", "block (action-poisoned): invalid action"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q in %s", text, &out)
		}
	}
}

func TestQueueDiscoveryStatus(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	body := `{"version":1,"discovery":{"last_successful_sweep":"2026-10-16T12:00:00Z","repositories":[{"installation":"7","repo":"owner/unconfigured","state":"not configured","last_successful_sweep":"2026-10-16T12:00:00Z"}],"installations":[{"installation":"7","suspended":false,"last_successful_sweep":"2026-10-16T12:00:00Z","budget":{"known":true,"limit":1000,"remaining":400,"settlement_reserve":100}}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/discovery" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	args := []string{"queue", "status", "--api", server.URL, "--discovery"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, &errOut)
	}
	for _, text := range []string{"owner/unconfigured", "not configured", "2026-10-16T12:00:00Z", "400/1000", "100 reserved for settlement"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q in %s", text, &out)
		}
	}
	out.Reset()
	if code := run(append(args, "--json"), &out, &errOut); code != 0 || out.String() != body {
		t.Fatal("discovery JSON changed")
	}
	if code := run(append(args, "--repo", "owner/repo"), io.Discard, &errOut); code != 2 {
		t.Fatal("conflicting selectors accepted")
	}
}

func TestQueueStatusShowsProcessReadiness(t *testing.T) {
	var out bytes.Buffer
	reason := "repository rules not yet verified: classic-protection read failed"
	response := status.Response{Queues: []status.Queue{{Repository: "owner/repo", Base: "main", State: "active", Readiness: &status.Readiness{Ready: false, Reason: reason}}}}
	if err := printStatus(&out, response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "waiting: "+reason) {
		t.Fatalf("readiness hidden: %s", &out)
	}
}

func TestQueueStatusPreservesInactiveLifecycle(t *testing.T) {
	for _, lifecycle := range []string{"dormant", "retired", "overlapping"} {
		var out bytes.Buffer
		response := status.Response{Queues: []status.Queue{{Repository: "owner/repo", Base: "main", State: lifecycle, Readiness: &status.Readiness{Ready: false, Reason: "validation pending"}}}}
		if err := printStatus(&out, response); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "waiting:") || !strings.Contains(out.String(), lifecycle) {
			t.Fatalf("lifecycle hidden: %s", out.String())
		}
	}
}

func TestDiscoveryPrintsWebhookHealthAndRepositoryAge(t *testing.T) {
	at := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	age := 125.0
	var out strings.Builder
	err := printDiscovery(&out, status.Discovery{
		Repositories:  []status.Repository{{Installation: "7", Name: "owner/active", LastDeliveryAgeSeconds: &age}, {Installation: "7", Name: "owner/quiet"}},
		Installations: []status.Installation{{ID: "7", WebhooksConfigured: true, WebhookHealthy: true, LastVerifiedDelivery: &at}, {ID: "8", WebhooksConfigured: true}, {ID: "9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LAST DELIVERY AGE", "2m5s", "owner/quiet", "never", "Installation 7 webhooks: healthy; last verified delivery: 2026-10-17T12:00:00Z", "Installation 8 webhooks: unhealthy", "Installation 9 webhooks: not configured"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, &out)
		}
	}
}

func TestQueueStatusShowsRunLedgerAndAdaptiveInputs(t *testing.T) {
	response := status.Response{Queue: &status.Queue{Repository: "owner/repo", Base: "main", Window: 4, BatchSize: 8, FailureEstimate: &status.FailureEstimate{Failures: 2, Samples: 20, Rate: .1, TargetPassRate: .8}, LastWindowFailure: &queue.WindowFailure{Revision: 25, BatchID: "b1", Reason: queue.ChecksFailed}, Chain: []*status.Batch{{ID: "b2", Position: 2, BatchesAhead: 1, TestedSHA: "tested", Phase: "testing"}}}, PR: &status.Entry{PR: 2, HeadSHA: "head", Gate: "checks", RunLedger: []status.Run{{BatchID: "b2", TestedSHA: "tested", TestRef: "openmerge/queue/main/b2", Outcome: "cancelled", WastedReason: "dequeue", Cancellation: "confirmed"}}}}
	var out bytes.Buffer
	if err := printStatus(&out, response); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"window: 4; batch size: 8", "p̂: 0.1000 (2/20)", "last window failure: revision 25 b1 checks-failed", "chain: b2(testing)", "batch b2 position 2", "Gate: checks", "CI run ledger:", "b2 tested openmerge/queue/main/b2 aggregate (identity unavailable): cancelled; wasted: dequeue; cancellation: confirmed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, &out)
		}
	}
}

func TestQueueStatusOutsidePushes(t *testing.T) {
	q := status.Queue{Repository: "owner/repo", Base: "main", State: "active", OutsidePushes: []status.OutsidePush{
		{EventID: 7, OutsidePushRecorded: queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "admin", AncestryKnown: true, HistoryKnown: true, Commits: []string{"admin"}, Truncated: true, VerificationBeforeLanding: "none"}},
		{EventID: 9, OutsidePushRecorded: queue.OutsidePushRecorded{PreviousSHA: "admin", NewSHA: "base", AncestryKnown: true, Rewrite: true, HistoryKnown: true, VerificationBeforeLanding: "none"}},
		{EventID: 10, OutsidePushRecorded: queue.OutsidePushRecorded{PreviousSHA: "base", NewSHA: "missing", VerificationBeforeLanding: "none"}},
		{EventID: 11, OutsidePushRecorded: queue.OutsidePushRecorded{PreviousSHA: "landed", NewSHA: "base", AncestryKnown: true, Rewrite: true, PossibleStaleObservation: true, VerificationBeforeLanding: "none"}},
	}}
	var output bytes.Buffer
	if err := printStatus(&output, status.Response{Version: status.Version, Queue: &q}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"outside push (event 7): base -> admin; fast-forward; commits [admin] (truncated)", "outside push (event 9): admin -> base; rewrite; commits []", "ancestry unknown; commits unknown", "OpenMerge verification before landing: none", "possible stale base observation: may reflect a stale read rather than a real rollback; verify before acknowledgement"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in %s", want, output.String())
		}
	}
}

type statusTimeoutTransport struct{ body bool }

func (transport statusTimeoutTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !transport.body {
		return nil, context.DeadlineExceeded
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: statusTimeoutBody{}, Request: request}, nil
}

type statusTimeoutBody struct{}

func (statusTimeoutBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (statusTimeoutBody) Close() error             { return nil }

func TestStatusTimeoutDiagnostic(t *testing.T) {
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	for _, body := range []bool{false, true} {
		http.DefaultTransport = statusTimeoutTransport{body: body}
		for _, args := range [][]string{
			{"queue", "status", "--api", "http://status.invalid", "--json"},
			{"change", "show", "chg1_" + strings.Repeat("a", 64), "--repo", "owner/repo", "--api", "http://status.invalid", "--json"},
		} {
			var out, errOut bytes.Buffer
			if code := run(args, &out, &errOut); code != 1 || out.Len() != 0 {
				t.Fatalf("%v body=%t: code=%d stdout=%s stderr=%s", args, body, code, &out, &errOut)
			}
			for _, want := range []string{"status API request timed out", "client limit 20s", "retry the read", "queue processing may still be running"} {
				if !strings.Contains(errOut.String(), want) {
					t.Fatalf("missing %q: %s", want, &errOut)
				}
			}
		}
	}
}
