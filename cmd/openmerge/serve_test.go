package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServeOptions(t *testing.T) {
	t.Setenv("OPENMERGE_DATABASE_URL", "postgres://local/test")
	t.Setenv("OPENMERGE_GITHUB_APP_ID", "42")
	t.Setenv("OPENMERGE_GITHUB_INSTALLATION_ID", "7")
	t.Setenv("OPENMERGE_GITHUB_PRIVATE_KEY", "key.pem")
	t.Setenv("OPENMERGE_REPOS", "owner/repo")
	t.Setenv("OPENMERGE_POLL_INTERVAL", "30s")
	options, err := parseServe([]string{"--poll-interval=5s"}, io.Discard)
	if err != nil || options.poll != 5*time.Second || options.app != 42 {
		t.Fatalf("options=%+v error=%v", options, err)
	}
	for _, args := range [][]string{{"--poll-interval=0"}, {"--concurrency=0"}, {"--webhook-listen=:8080"}, {"--repos=invalid"}, {"unexpected"}} {
		if _, err := parseServe(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	t.Setenv("OPENMERGE_POLL_INTERVAL", "broken")
	if _, err := parseServe(nil, io.Discard); err == nil {
		t.Fatal("malformed env accepted")
	}
	if _, err := parseServe([]string{"--poll-interval=5s"}, io.Discard); err != nil {
		t.Fatalf("explicit flag did not override env: %v", err)
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
