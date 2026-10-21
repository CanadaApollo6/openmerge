package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/gitops"
)

type committerFake struct {
	identity gitops.Identity
	err      error
	calls    int
}

func (*committerFake) BotLogin() string { return "queue-app[bot]" }
func (f *committerFake) Committer(context.Context) (gitops.Identity, error) {
	f.calls++
	return f.identity, f.err
}

func TestServeCommitter(t *testing.T) {
	discovered := gitops.Identity{Name: "queue-app[bot]", Email: "99+queue-app[bot]@users.noreply.github.com"}
	for _, tc := range []struct {
		name    string
		options serveOptions
		failed  bool
		want    gitops.Identity
		calls   int
		warn    bool
	}{
		{"discovered", serveOptions{}, false, discovered, 1, false},
		{"full override", serveOptions{committerName: "Operator", committerEmail: "op@example.test"}, true, gitops.Identity{Name: "Operator", Email: "op@example.test"}, 0, false},
		{"name override", serveOptions{committerName: "Operator"}, false, gitops.Identity{Name: "Operator", Email: discovered.Email}, 1, false},
		{"email override", serveOptions{committerEmail: "op@example.test"}, true, gitops.Identity{Name: discovered.Name, Email: "op@example.test"}, 0, false},
		{"fallback", serveOptions{}, true, gitops.Identity{Name: discovered.Name, Email: "queue-app[bot]@users.noreply.github.com"}, 1, true},
		{"fallback with override", serveOptions{committerName: "Operator"}, true, gitops.Identity{Name: "Operator", Email: "queue-app[bot]@users.noreply.github.com"}, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driver := &committerFake{identity: discovered}
			if tc.failed {
				driver.err = errors.New("lookup unavailable")
			}
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			if got := serveCommitter(t.Context(), tc.options, logger, driver); got != tc.want || driver.calls != tc.calls {
				t.Fatalf("committer=%+v calls=%d, want %+v calls=%d", got, driver.calls, tc.want, tc.calls)
			}
			if tc.warn {
				for _, text := range []string{"level=WARN", "OPENMERGE_GIT_COMMITTER_NAME=queue-app[bot]", "OPENMERGE_GIT_COMMITTER_EMAIL=<bot-user-id>+queue-app[bot]@users.noreply.github.com", "GET /users/queue-app[bot]"} {
					if !strings.Contains(logs.String(), text) {
						t.Fatalf("fallback log missing %q: %s", text, logs.String())
					}
				}
			} else if logs.Len() != 0 {
				t.Fatalf("unexpected warning: %s", logs.String())
			}
		})
	}
}

func TestServeDatabaseErrorKeepsCause(t *testing.T) {
	err := startServe(t.Context(), serveOptions{database: "postgres://localhost/db?port=invalid"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err == nil || errors.Unwrap(err) == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Fatalf("database cause lost: %v", err)
	}
}

func TestServeOptions(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://local/test")
	t.Setenv("OPENMERGE_GITHUB_APP_ID", "42")
	t.Setenv("OPENMERGE_GITHUB_INSTALLATION_ID", "7")
	t.Setenv("OPENMERGE_GITHUB_PRIVATE_KEY", "key.pem")
	t.Setenv("OPENMERGE_REPOS", "owner/repo")
	t.Setenv("OPENMERGE_POLL_INTERVAL", "30s")
	t.Setenv("OPENMERGE_LOG_LEVEL", "debug")
	t.Setenv("OPENMERGE_GIT_COMMITTER_NAME", "queue-app[bot]")
	t.Setenv("OPENMERGE_GIT_COMMITTER_EMAIL", "99+queue-app[bot]@users.noreply.github.com")
	t.Setenv("OPENMERGE_API_LISTEN", "")
	t.Setenv("OPENMERGE_API_TOKEN", "")
	t.Setenv("OPENMERGE_API_TOKEN_FILE", "")
	t.Setenv("OPENMERGE_DELETE_HEAD_BRANCH", "true")
	options, err := parseServe([]string{"--poll-interval=5s"}, io.Discard)
	if err != nil || options.poll != 5*time.Second || options.app != 42 || options.logLevel != "debug" || options.committerName != "queue-app[bot]" || options.committerEmail != "99+queue-app[bot]@users.noreply.github.com" {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	if options.apiListen != "" || options.rulesInterval != 10*time.Minute {
		t.Fatal("API enabled by default or unexpected rules interval")
	}
	if !options.deleteHeadBranch {
		t.Fatal("cleanup default disabled")
	}
	t.Setenv("OPENMERGE_DELETE_HEAD_BRANCH", "false")
	if got, err := parseServe(nil, io.Discard); err != nil || got.deleteHeadBranch {
		t.Fatalf("cleanup env: %+v %v", got, err)
	}
	t.Setenv("OPENMERGE_DELETE_HEAD_BRANCH", "invalid")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("invalid cleanup env accepted")
	}
	if got, err := parseServe([]string{"--delete-head-branch=true"}, io.Discard); err != nil || !got.deleteHeadBranch {
		t.Fatalf("cleanup flag override: %+v %v", got, err)
	}
	t.Setenv("OPENMERGE_DELETE_HEAD_BRANCH", "true")
	if got, err := parseServe([]string{"--delete-head-branch=false"}, io.Discard); err != nil || got.deleteHeadBranch {
		t.Fatalf("cleanup flag: %+v %v", got, err)
	}
	if got, err := parseServe([]string{"--api-listen=:8081", "--rules-interval=2m"}, io.Discard); err != nil || got.apiListen != "127.0.0.1:8081" || got.rulesInterval != 2*time.Minute {
		t.Fatalf("API/rules options=%+v %v", got, err)
	}
	for _, address := range []string{"0.0.0.0:8081", "[::]:8081"} {
		if _, err := parseServe([]string{"--api-listen=" + address}, io.Discard); err == nil || !strings.Contains(err.Error(), "requires a bearer token") {
			t.Fatalf("unauthenticated wildcard bind %s: %v", address, err)
		}
		t.Setenv("OPENMERGE_API_TOKEN", "secret")
		if got, err := parseServe([]string{"--api-listen=" + address}, io.Discard); err != nil || got.apiListen != address || got.apiToken != "secret" {
			t.Fatalf("authenticated wildcard bind %s: %+v %v", address, got, err)
		}
		t.Setenv("OPENMERGE_API_TOKEN", "")
	}
	for _, args := range [][]string{{"--log-level=bogus"}, {"--poll-interval=0"}, {"--rules-interval=0"}, {"--api-listen=8081"}, {"--concurrency=0"}, {"--webhook-listen=:8080"}, {"--repos=invalid"}, {"unexpected"}} {
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	t.Setenv("OPENMERGE_LOG_LEVEL", "bogus")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("malformed log level env accepted")
	}
	if options, err := parseServe([]string{"--log-level=warn"}, io.Discard); err != nil || options.logLevel != "warn" {
		t.Fatalf("log flag did not override env: %+v %v", options, err)
	}
	t.Setenv("OPENMERGE_LOG_LEVEL", "info")
	t.Setenv("OPENMERGE_POLL_INTERVAL", "broken")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("malformed env accepted")
	}
	if _, err := parseServe([]string{"--poll-interval=5s"}, io.Discard); err != nil {
		t.Fatalf("explicit flag did not override env: %v", err)
	}
	if _, err := parseServe([]string{"--git-cache-dir", "--poll-interval=5s"}, io.Discard); err == nil {
		t.Fatal("a flag-like string value incorrectly overrode the poll environment")
	}
}

type notification struct {
	calls                        int
	ignoredCalls                 int
	installation, repo, delivery string
	err                          error
}

func (n *notification) Notify(_ context.Context, installation, repo, delivery string) error {
	n.calls++
	n.installation, n.repo, n.delivery = installation, repo, delivery
	return n.err
}

func (n *notification) NotifyIgnored(ctx context.Context, installation, repo, delivery string) error {
	n.ignoredCalls++
	return n.Notify(ctx, installation, repo, delivery)
}

type branchNotification struct {
	notification
	branchCalls int
}

func (n *branchNotification) NotifyBranches(ctx context.Context, installation, repo, delivery string) error {
	n.branchCalls++
	return n.Notify(ctx, installation, repo, delivery)
}

func TestOnlyBranchCreateDeleteWebhooksRequestListings(t *testing.T) {
	for _, event := range []string{"push", "create", "delete"} {
		t.Run(event, func(t *testing.T) {
			body := `{"ref":"release/v1","ref_type":"branch","after":"abc123","installation":{"id":7},"repository":{"full_name":"owner/repo"}}`
			if event == "push" {
				body = strings.Replace(body, `"release/v1"`, `"refs/heads/release/v1"`, 1)
			}
			secret := []byte("test-secret")
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(body))
			receiver := &branchNotification{}
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			r.Header.Set("X-GitHub-Event", event)
			r.Header.Set("X-GitHub-Delivery", "branch-delivery")
			response := httptest.NewRecorder()
			webhookHandler(receiver, secret).ServeHTTP(response, r)
			wantBranches := 1
			if event == "push" {
				wantBranches = 0
			}
			if response.Code != http.StatusAccepted || receiver.calls != 1 || receiver.branchCalls != wantBranches {
				t.Fatalf("routing %s: response=%d receiver=%+v", event, response.Code, receiver)
			}
		})
	}
}

func TestWebhookOnlySchedulesReconciliation(t *testing.T) {
	body := `{"action":"created","installation":{"id":7},"repository":{"full_name":"owner/repo"},"issue":{"number":1,"pull_request":{}},"comment":{"body":"@openmerge queue","user":{"login":"untrusted"}}}`
	secret := []byte("webhook-test")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	receiver := &notification{}
	handler := webhookHandler(receiver, secret)
	request := func(signature string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", signature)
		r.Header.Set("X-GitHub-Event", "issue_comment")
		r.Header.Set("X-GitHub-Delivery", "delivery")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	signed := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := request(signed).Code; got != http.StatusAccepted {
		t.Fatalf("signed delivery=%d", got)
	}
	if receiver.calls != 1 || receiver.ignoredCalls != 0 || receiver.installation != "7" || receiver.repo != "owner/repo" || receiver.delivery != "delivery" {
		t.Fatalf("routing=%+v", receiver)
	}
	if got := request("sha256=bad").Code; got != http.StatusUnauthorized || receiver.calls != 1 {
		t.Fatal("invalid signature triggered work")
	}
	receiver.err = errors.New("database unavailable")
	if got := request(signed).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("unpersisted delivery acknowledged: %d", got)
	}
}

func TestServeBaseIsOptionalAllowlistAndDiscoveryInterval(t *testing.T) {
	for name, value := range map[string]string{"DATABASE_URL": "postgres://local/test", "GITHUB_APP_ID": "42", "GITHUB_INSTALLATION_ID": "7", "GITHUB_PRIVATE_KEY": "key.pem", "REPOS": "owner/repo", "BASE": "", "DISCOVERY_INTERVAL": "2m"} {
		t.Setenv("OPENMERGE_"+name, value)
	}
	options, err := parseServe(nil, io.Discard)
	if err != nil || options.base != "" || options.discoveryInterval != 2*time.Minute {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	options, err = parseServe([]string{"--base=main,release/*", "--discovery-interval=30s"}, io.Discard)
	if err != nil || options.base != "main,release/*" || options.discoveryInterval != 30*time.Second {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	for _, args := range [][]string{{"--base=main,"}, {"--base=release/\\"}, {"--discovery-interval=0"}} {
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestDiscoveryServeAllowlistsOptionalAndNarrowing(t *testing.T) {
	for key, value := range map[string]string{"DATABASE_URL": "postgres://local/test", "GITHUB_APP_ID": "42", "GITHUB_PRIVATE_KEY": "key.pem", "GITHUB_INSTALLATION_ID": "", "REPOS": "", "BASE": ""} {
		t.Setenv("OPENMERGE_"+key, value)
	}
	// Empty string is malformed for an explicitly supplied typed env value.
	t.Setenv("OPENMERGE_GITHUB_INSTALLATION_ID", "0")
	o, err := parseServe(nil, io.Discard)
	if err != nil || o.installation != 0 || o.repos != "" || o.base != "" || o.discoveryInterval != 10*time.Minute {
		t.Fatalf("discovery defaults=%+v %v", o, err)
	}
	for _, pattern := range []string{"Owner/*", "*/repo?", "owner/repo,Other/*"} {
		if _, err := parseServe([]string{"--repos=" + pattern}, io.Discard); err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
	}
	if _, err := parseServe([]string{"--github-installation-id=-1"}, io.Discard); err == nil {
		t.Fatal("negative installation accepted")
	}
}

type discoveryNotification struct {
	notification
	discoveryCalls int
}

func (n *discoveryNotification) NotifyDiscovery(_ context.Context, _, _, _ string) error {
	n.discoveryCalls++
	return nil
}

func TestInstallationWebhooksOnlyRequestDiscovery(t *testing.T) {
	for _, event := range []string{"installation", "installation_repositories"} {
		n := &discoveryNotification{}
		body := `{"action":"deleted","installation":{"id":7},"repositories_removed":[{"full_name":"owner/repo"}]}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", "delivery")
		mac := hmac.New(sha256.New, []byte("secret"))
		_, _ = mac.Write([]byte(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		response := httptest.NewRecorder()
		webhookHandler(n, []byte("secret")).ServeHTTP(response, req)
		if response.Code != http.StatusAccepted || n.discoveryCalls != 1 || n.calls != 0 {
			t.Fatalf("payload changed membership: status=%d discovery=%d notifications=%d", response.Code, n.discoveryCalls, n.calls)
		}
	}
}

func TestWebhookIgnoredDeliveriesAndUnsignedFlood(t *testing.T) {
	secret := []byte("test-secret")
	receiver := &notification{}
	var logs strings.Builder
	handler := webhookHandler(receiver, secret, slog.New(slog.NewTextHandler(&logs, nil)))
	body := `{"action":"assigned","installation":{"id":7},"repository":{"full_name":"owner/repo"}}`
	deliver := func(kind, signature string) int {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("X-Hub-Signature-256", signature)
		r.Header.Set("X-GitHub-Event", kind)
		r.Header.Set("X-GitHub-Delivery", "ignored-delivery")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for range 4096 {
		if got := deliver("pull_request", "sha256=bad"); got != http.StatusUnauthorized {
			t.Fatalf("unsigned request: %d", got)
		}
	}
	// Notify is the only path from the HTTP boundary into health, timers,
	// persistence and forge work. No rejected request may enter that path.
	if receiver.calls != 0 || receiver.installation != "" {
		t.Fatal("unverified flood changed health/scheduling or requested forge work")
	}
	if count := strings.Count(logs.String(), "webhook signature rejections"); count != 13 || !strings.Contains(logs.String(), "count=4096") {
		t.Fatalf("rejections were not counted with bounded log volume: %s", logs.String())
	}
	if strings.Contains(logs.String(), body) || strings.Contains(logs.String(), "sha256=bad") {
		t.Fatal("rejection counter included untrusted request data")
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	signed := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	for _, kind := range []string{"pull_request", "unknown_future_event"} {
		if got := deliver(kind, signed); got != http.StatusAccepted {
			t.Fatalf("verified %s: %d", kind, got)
		}
	}
	// A signed but incomplete typed payload is still an authenticated envelope.
	body = strings.Replace(body, "assigned", "opened", 1)
	mac = hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(body))
	if got := deliver("pull_request", "sha256="+hex.EncodeToString(mac.Sum(nil))); got != http.StatusBadRequest {
		t.Fatalf("incomplete typed payload: %d", got)
	}
	if receiver.calls != 3 || receiver.ignoredCalls != 3 || receiver.installation != "7" || receiver.repo != "owner/repo" {
		t.Fatalf("ignored deliveries failed to record/wake their repository: %+v", receiver)
	}
}

func TestWebhookSchedulingSettings(t *testing.T) {
	for key, value := range map[string]string{"DATABASE_URL": "postgres://local/test", "GITHUB_APP_ID": "42", "GITHUB_PRIVATE_KEY": "key.pem", "BACKSTOP_INTERVAL": "10m", "WEBHOOK_HEALTH_WINDOW": "30m"} {
		t.Setenv("OPENMERGE_"+key, value)
	}
	o, err := parseServe(nil, io.Discard)
	if err != nil || o.backstop != 10*time.Minute || o.webhookHealthWindow != 30*time.Minute {
		t.Fatalf("settings = %+v, %v", o, err)
	}
	t.Setenv("OPENMERGE_BACKSTOP_INTERVAL", "3m")
	t.Setenv("OPENMERGE_WEBHOOK_HEALTH_WINDOW", "9m")
	o, err = parseServe(nil, io.Discard)
	if err != nil || o.backstop != 3*time.Minute || o.webhookHealthWindow != 9*time.Minute {
		t.Fatalf("environment = %+v, %v", o, err)
	}
	for _, flag := range []string{"backstop-interval", "webhook-health-window"} {
		for _, value := range []string{"0", "-1m", "invalid"} {
			if _, err := parseServe([]string{"--" + flag + "=" + value}, io.Discard); err == nil {
				t.Fatalf("accepted %s=%s", flag, value)
			}
		}
	}
	t.Setenv("OPENMERGE_BACKSTOP_INTERVAL", "invalid")
	t.Setenv("OPENMERGE_WEBHOOK_HEALTH_WINDOW", "invalid")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("invalid environment accepted")
	}
	o, err = parseServe([]string{"--backstop-interval=4m", "--webhook-health-window=8m"}, io.Discard)
	if err != nil || o.backstop != 4*time.Minute || o.webhookHealthWindow != 8*time.Minute {
		t.Fatalf("overrides = %+v, %v", o, err)
	}
}

func TestServeHealthThresholdBounds(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://unused/test")
	t.Setenv("OPENMERGE_GITHUB_APP_ID", "42")
	t.Setenv("OPENMERGE_GITHUB_PRIVATE_KEY", "unused.pem")
	t.Setenv("OPENMERGE_API_LISTEN", "")
	t.Setenv("OPENMERGE_HEALTH_OVERDUE_GRACE", "30s")
	t.Setenv("OPENMERGE_HEALTH_MAX_SUCCESS_AGE", "15m")
	t.Setenv("OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT", "4m")
	for _, args := range [][]string{{"--health-progress-stall-limit=0s"}, {"--health-progress-stall-limit=150s"}, {"--health-progress-stall-limit=31m"}, {"--health-max-cycle-duration=1m"}, {"--health-max-cycle-duration=7h"}, {"--health-overdue-grace=0s"}, {"--health-overdue-grace=61s"}, {"--health-max-success-age=59s"}, {"--health-max-success-age=61m"}} {
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	o, err := parseServe([]string{"--health-overdue-grace=1s", "--health-max-success-age=60m", "--health-max-cycle-duration=2h", "--health-progress-stall-limit=3m"}, io.Discard)
	if err != nil || o.healthGrace != time.Second || o.healthMaxAge != time.Hour || o.healthMaxCycle != 2*time.Hour || o.healthProgressStall != 3*time.Minute {
		t.Fatalf("%+v %v", o, err)
	}
	o, err = parseServe(nil, io.Discard)
	if err != nil || o.healthProgressStall != 4*time.Minute {
		t.Fatalf("stall environment: %+v %v", o, err)
	}
	t.Setenv("OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT", "150s")
	if _, err := parseServe(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "git maintenance") {
		t.Fatalf("accepted incompatible environment: %v", err)
	}
	if err := os.Unsetenv("OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT"); err != nil {
		t.Fatal(err)
	}
	o, err = parseServe(nil, io.Discard)
	if err != nil || o.healthProgressStall != 4*time.Minute {
		t.Fatalf("default stall: %+v %v", o, err)
	}
	t.Setenv("OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT", "bad")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("accepted malformed stall env")
	}
	t.Setenv("OPENMERGE_HEALTH_PROGRESS_STALL_LIMIT", "4m")
	t.Setenv("OPENMERGE_HEALTH_MAX_SUCCESS_AGE", "bad")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("accepted malformed threshold env")
	}
}
