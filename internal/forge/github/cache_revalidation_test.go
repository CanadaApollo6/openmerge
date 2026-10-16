package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidatorless304CannotReplayAuthorizationOrCI(t *testing.T) {
	for _, mode := range []string{"permission", "checks"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPI(t)
			passingCI(f)
			if mode == "permission" {
				ok, err := f.driver.CanWrite(context.Background(), testRepo, "writer")
				must(t, err)
				if !ok {
					t.Fatal("fixture did not grant write permission")
				}
			} else {
				events, err := reviewRefresh(f)
				must(t, err)
				if !reviewCompletedSuccess(events) {
					t.Fatal("fixture did not pass CI")
				}
			}
			f.mu.Lock()
			f.permissions["writer"] = "read"
			f.runs["tested"][0].Conclusion = new("failure")
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if mode == "permission" && strings.HasSuffix(r.URL.Path, "/permission") || mode == "checks" && strings.HasSuffix(r.URL.Path, "/check-runs") {
					if r.Header.Get("If-None-Match") == "" {
						t.Error("request did not revalidate the cached result")
					}
					w.WriteHeader(http.StatusNotModified)
					return true
				}
				return false
			}
			f.mu.Unlock()
			if mode == "permission" {
				ok, err := f.driver.CanWrite(context.Background(), testRepo, "writer")
				if err == nil || ok {
					t.Fatalf("validatorless 304 replayed revoked permission: ok=%v err=%v", ok, err)
				}
			} else {
				events, err := reviewRefresh(f)
				if err == nil || reviewCompletedSuccess(events) {
					t.Fatalf("validatorless 304 replayed failing CI: events=%+v err=%v", events, err)
				}
			}
		})
	}
}

func TestConditionalGETUsesWeakEntityTagComparison(t *testing.T) {
	for _, tags := range [][]string{
		{`"v1"`, `W/"v1"`, `"v1"`},
		{`W/"v1"`, `"v1"`, `W/"v1"`},
		{`""`, `W/""`, `""`},
	} {
		t.Run(strings.Join(tags, "/"), func(t *testing.T) {
			calls := 0
			tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("ETag", tags[calls])
				status, body := http.StatusOK, "body"
				if calls > 0 {
					if got := r.Header.Get("If-None-Match"); got != tags[calls-1] {
						t.Fatalf("conditional=%q, want %q", got, tags[calls-1])
					}
					status, body = http.StatusNotModified, ""
				}
				calls++
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))
			for range tags {
				req, err := http.NewRequest(http.MethodGet, "https://api.test/items", nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				must(t, err)
				body, err := io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
				if res.StatusCode != http.StatusOK || string(body) != "body" {
					t.Fatalf("weakly matching 304 lost representation: status=%d body=%q", res.StatusCode, body)
				}
			}
		})
	}
}
