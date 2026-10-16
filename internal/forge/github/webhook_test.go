package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

var webhookSecret = []byte("test-webhook-secret")

func signedRequest(kind string, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "https://openmerge.test/webhooks", bytes.NewReader(body))
	mac := hmac.New(sha256.New, webhookSecret)
	_, _ = mac.Write(body)
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Delivery", "delivery-id")
	r.Header.Set("X-GitHub-Event", kind)
	return r
}
func webhookFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name + ".json")
	must(t, err)
	return body
}
func TestWebhookFixtures(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		kind    string
		payload Payload
	}{
		{"pull_request", PRChanged{Action: "synchronize", Number: 17, HeadSHA: sha, Base: "main"}},
		{"pull_request_review", ReviewChanged{Action: "submitted", Number: 17, HeadSHA: sha, ReviewedSHA: sha, Author: "maintainer", State: "approved"}},
		{"check_run", ChecksCompleted{SHA: sha}}, {"check_suite", ChecksCompleted{SHA: sha}}, {"status", ChecksCompleted{SHA: sha}},
		{"push", BranchPushed{Ref: "refs/heads/main", SHA: sha}},
		{"issue_comment", PRCommentCreated{Number: 17, Author: "maintainer", Body: "@openmerge enqueue", AuthorAssociation: "COLLABORATOR"}},
		{"installation", InstallationChanged{Action: "created", Repositories: repoRefs([]webhookRepository{{FullName: "acme/project"}}), Added: repoRefs(nil), Removed: repoRefs(nil)}},
		{"installation_repositories", InstallationChanged{Action: "added", Repositories: repoRefs(nil), Added: repoRefs([]webhookRepository{{FullName: "acme/project"}}), Removed: repoRefs(nil)}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e, err := VerifyAndParse(signedRequest(tc.kind, webhookFixture(t, tc.kind)), webhookSecret)
			must(t, err)
			if e.DeliveryID != "delivery-id" || e.InstallationID != 7 || !reflect.DeepEqual(e.Payload, tc.payload) {
				t.Fatalf("normalized event=%+v, payload=%+v; want %+v", e, e.Payload, tc.payload)
			}
			if !strings.HasPrefix(tc.kind, "installation") && e.Repo != testRepo {
				t.Fatal("repository missing")
			}
		})
	}
}
func mutateFixture(t *testing.T, kind string, mutate func(map[string]any)) []byte {
	t.Helper()
	var raw map[string]any
	must(t, json.Unmarshal(webhookFixture(t, kind), &raw))
	mutate(raw)
	data, err := json.Marshal(raw)
	must(t, err)
	return data
}
func TestWebhookActions(t *testing.T) {
	for _, action := range []string{"opened", "synchronize", "reopened", "closed", "edited", "labeled", "unlabeled", "assigned"} {
		t.Run("pr/"+action, func(t *testing.T) {
			body := mutateFixture(t, "pull_request", func(raw map[string]any) { raw["action"] = action })
			e, err := VerifyAndParse(signedRequest("pull_request", body), webhookSecret)
			must(t, err)
			_, ignored := e.Payload.(Ignored)
			if ignored != (action == "assigned") {
				t.Fatalf("action %s=%+v", action, e.Payload)
			}
		})
	}
	for _, action := range []string{"submitted", "dismissed", "edited"} {
		body := mutateFixture(t, "pull_request_review", func(raw map[string]any) { raw["action"] = action })
		e, err := VerifyAndParse(signedRequest("pull_request_review", body), webhookSecret)
		must(t, err)
		_, ignored := e.Payload.(Ignored)
		if ignored != (action == "edited") {
			t.Fatalf("review %s=%+v", action, e.Payload)
		}
	}
	for _, state := range []string{"success", "failure", "error", "pending"} {
		body := mutateFixture(t, "status", func(raw map[string]any) { raw["state"] = state })
		e, err := VerifyAndParse(signedRequest("status", body), webhookSecret)
		must(t, err)
		_, ignored := e.Payload.(Ignored)
		if ignored != (state == "pending") {
			t.Fatalf("status %s=%+v", state, e.Payload)
		}
	}
	for _, kind := range []string{"check_run", "check_suite"} {
		body := mutateFixture(t, kind, func(raw map[string]any) { raw["action"] = "requested" })
		e, err := VerifyAndParse(signedRequest(kind, body), webhookSecret)
		must(t, err)
		if _, ok := e.Payload.(Ignored); !ok {
			t.Fatal("unfinished check was not ignored")
		}
	}
	for _, action := range []string{"created", "deleted", "suspend", "unsuspend", "new_permissions_accepted"} {
		body := mutateFixture(t, "installation", func(raw map[string]any) { raw["action"] = action })
		e, err := VerifyAndParse(signedRequest("installation", body), webhookSecret)
		must(t, err)
		if p, ok := e.Payload.(InstallationChanged); !ok || p.Action != action {
			t.Fatal("installation action lost")
		}
	}
	for _, tc := range []struct {
		kind   string
		mutate func(map[string]any)
	}{
		{"push", func(raw map[string]any) { raw["ref"] = "refs/tags/v1" }},
		{"issue_comment", func(raw map[string]any) { delete(raw["issue"].(map[string]any), "pull_request") }},
		{"issue_comment", func(raw map[string]any) { raw["action"] = "edited" }},
	} {
		body := mutateFixture(t, tc.kind, tc.mutate)
		e, err := VerifyAndParse(signedRequest(tc.kind, body), webhookSecret)
		must(t, err)
		if _, ok := e.Payload.(Ignored); !ok {
			t.Fatalf("unexpected payload %+v", e.Payload)
		}
	}
	body := mutateFixture(t, "push", func(raw map[string]any) { raw["deleted"] = true; raw["after"] = strings.Repeat("0", 40) })
	e, err := VerifyAndParse(signedRequest("push", body), webhookSecret)
	must(t, err)
	if !e.Payload.(BranchPushed).Deleted {
		t.Fatal("branch deletion lost")
	}
	e, err = VerifyAndParse(signedRequest("unknown_future_event", []byte("uninterpreted")), webhookSecret)
	must(t, err)
	if p, ok := e.Payload.(Ignored); !ok || p.Event != "unknown_future_event" {
		t.Fatal("unknown event not typed ignored")
	}
}
func TestWebhookRejections(t *testing.T) {
	body := webhookFixture(t, "pull_request")
	for _, tc := range []struct {
		name   string
		edit   func(*http.Request)
		secret []byte
		code   int
	}{
		{"method", func(r *http.Request) { r.Method = "GET" }, webhookSecret, 405},
		{"missing signature", func(r *http.Request) { r.Header.Del("X-Hub-Signature-256") }, webhookSecret, 401},
		{"wrong algorithm", func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha1="+strings.Repeat("0", 64)) }, webhookSecret, 401},
		{"invalid hex", func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("x", 64)) }, webhookSecret, 401},
		{"short digest", func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256=00") }, webhookSecret, 401},
		{"wrong secret", func(*http.Request) {}, []byte("wrong"), 401},
		{"empty secret", func(*http.Request) {}, nil, 401},
		{"tampered body", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{}")) }, webhookSecret, 401},
		{"missing delivery", func(r *http.Request) { r.Header.Del("X-GitHub-Delivery") }, webhookSecret, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := signedRequest("pull_request", body)
			tc.edit(r)
			_, err := VerifyAndParse(r, tc.secret)
			var failure *WebhookError
			if !errors.As(err, &failure) || failure.StatusCode != tc.code {
				t.Fatalf("rejection=%v", err)
			}
		})
	}
	// Exercise every digest byte. Do not assert wall-clock timings: scheduler noise
	// cannot prove constant time. Production uses crypto/hmac.Equal for comparison.
	for index := range sha256.Size {
		r := signedRequest("pull_request", body)
		digest, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		must(t, err)
		digest[index] ^= 1
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(digest))
		if _, err = VerifyAndParse(r, webhookSecret); err == nil {
			t.Fatalf("accepted mismatch at byte %d", index)
		}
	}
	for _, invalid := range []string{"{", `{"action":"opened"}`} {
		_, err := VerifyAndParse(signedRequest("pull_request", []byte(invalid)), webhookSecret)
		if err == nil {
			t.Fatal("malformed supported payload accepted")
		}
	}
	r := signedRequest("pull_request", body)
	r.Body = io.NopCloser(io.LimitReader(zeroReader{}, MaxWebhookBody+1))
	_, err := VerifyAndParse(r, webhookSecret)
	var failure *WebhookError
	if !errors.As(err, &failure) || failure.StatusCode != 413 {
		t.Fatalf("oversized payload=%v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
