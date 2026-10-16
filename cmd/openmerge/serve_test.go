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
	err := startServe(t.Context(), serveOptions{database: "postgres://localhost/db?port=invalid"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	options, err := parseServe([]string{"--poll-interval=5s"}, io.Discard)
	if err != nil || options.poll != 5*time.Second || options.app != 42 || options.logLevel != "debug" || options.committerName != "queue-app[bot]" || options.committerEmail != "99+queue-app[bot]@users.noreply.github.com" {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	for _, args := range [][]string{{"--log-level=bogus"}, {"--poll-interval=0"}, {"--concurrency=0"}, {"--webhook-listen=:8080"}, {"--repos=invalid"}, {"unexpected"}} {
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
	installation, repo, delivery string
	err                          error
}

func (n *notification) Notify(_ context.Context, installation, repo, delivery string) error {
	n.calls++
	n.installation, n.repo, n.delivery = installation, repo, delivery
	return n.err
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
	if receiver.calls != 1 || receiver.installation != "7" || receiver.repo != "owner/repo" || receiver.delivery != "delivery" {
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
