package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
)

func TestConditionalGETReplaysPaginatedSDKResponses(t *testing.T) {
	f := newAPI(t)
	for i := range 5 {
		f.comments[1] = append(f.comments[1], &gh.IssueComment{ID: new(int64(i + 1)), Body: new(fmt.Sprint(i)), User: &gh.User{Login: new("writer"), Type: new("User")}})
	}
	ctx := context.Background()
	first, err := f.driver.ListComments(ctx, testRepo, 1, time.Time{})
	must(t, err)
	second, err := f.driver.ListComments(ctx, testRepo, 1, time.Time{})
	must(t, err)
	if len(first) != 5 || !reflect.DeepEqual(first, second) || f.notModified != 3 {
		t.Fatalf("304 lost bodies or pagination: first=%+v second=%+v 304s=%d", first, second, f.notModified)
	}
	f.comments[1][0].Body = new("changed")
	third, err := f.driver.ListComments(ctx, testRepo, 1, time.Time{})
	must(t, err)
	if third[0].Body != "changed" || f.notModified != 5 {
		t.Fatalf("ETag did not revalidate changed page: %+v, 304s=%d", third, f.notModified)
	}
}

func cacheTransport(t *testing.T, entries, bytes int, base http.RoundTripper) *transport {
	t.Helper()
	origin, err := url.Parse("https://api.test/")
	must(t, err)
	return &transport{base: base, origin: origin, now: time.Now, cache: newResponseCache(entries, bytes), token: func(context.Context) (string, error) { return "first-token", nil }}
}

func TestConditionalGETIdentityAndRepresentationIsolation(t *testing.T) {
	var seen []string
	tr := cacheTransport(t, 20, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		identity := r.Header.Get("Authorization") + "/" + r.URL.String() + "/" + r.Header.Get("Accept") + "/" + r.Header.Get("X-GitHub-Api-Version")
		seen = append(seen, r.Header.Get("If-None-Match"))
		h := make(http.Header)
		h.Set("ETag", `"validator"`)
		h.Set("Vary", "Accept, Authorization, Accept-Encoding, X-Requested-With, X-GitHub-Api-Version")
		code := 200
		if r.Header.Get("If-None-Match") != "" {
			code = 304
			identity = ""
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(identity)), Request: r}, nil
	}))
	read := func(path, accept, version string) string {
		t.Helper()
		req, err := http.NewRequest("GET", "https://api.test/"+path, nil)
		must(t, err)
		req.Header.Set("Accept", accept)
		req.Header.Set("X-GitHub-Api-Version", version)
		res, err := tr.RoundTrip(req)
		must(t, err)
		body, err := io.ReadAll(res.Body)
		must(t, err)
		must(t, res.Body.Close())
		if res.StatusCode != 200 {
			t.Fatalf("SDK would see %d", res.StatusCode)
		}
		return string(body)
	}
	first := read("items?page=1", "json", "v1")
	if read("items?page=1", "json", "v1") != first {
		t.Fatal("304 body was lost")
	}
	otherURL := read("items?page=2", "json", "v1")
	otherAccept := read("items?page=1", "raw", "v1")
	otherVersion := read("items?page=1", "json", "v2")
	tr.token = func(context.Context) (string, error) { return "second-token", nil }
	second := read("items?page=1", "json", "v1")
	if second == first || otherURL == first || otherAccept == first || otherVersion == first {
		t.Fatal("cache crossed token, URL, or representation boundary")
	}
	if !reflect.DeepEqual(seen, []string{"", `"validator"`, "", "", "", ""}) {
		t.Fatalf("validators leaked: %q", seen)
	}
	if read("items?page=1", "json", "v1") != second {
		t.Fatal("rotated credential did not revalidate")
	}
}

func TestConditionalGETLRUEviction(t *testing.T) {
	for _, mode := range []string{"entries", "bytes"} {
		t.Run(mode, func(t *testing.T) {
			entries, bytes := 2, 10000
			if mode == "bytes" {
				entries, bytes = 100, 400
			}
			var conditional []bool
			tr := cacheTransport(t, entries, bytes, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				conditional = append(conditional, r.Header.Get("If-None-Match") != "")
				h := make(http.Header)
				h.Set("ETag", `"v1"`)
				code := 200
				body := strings.Repeat("x", 80)
				if r.Header.Get("If-None-Match") != "" {
					code = 304
					body = ""
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			read := func(path string) {
				t.Helper()
				req, err := http.NewRequest("GET", "https://api.test/"+path, nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				must(t, err)
				_, err = io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
			}
			// Refresh a, then add c; b is the least recently used entry.
			for _, path := range []string{"a", "b", "a", "c", "a", "b"} {
				read(path)
			}
			if !reflect.DeepEqual(conditional, []bool{false, false, true, false, true, false}) {
				t.Fatalf("LRU eviction failed: %v", conditional)
			}
			if tr.cache.bytes > bytes || tr.cache.lru.Len() > entries {
				t.Fatal("cache exceeded its bound")
			}
		})
	}
}

func TestConditionalGETNeverReplaysFailuresOrUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"error", "oversized", "no-etag", "no-store", "vary-star", "write", "unsolicited-304"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			tr := cacheTransport(t, 4, 300, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				h := make(http.Header)
				h.Set("ETag", `"v1"`)
				code, body := 200, "original"
				if requests > 1 {
					body = "new"
				}
				switch mode {
				case "error":
					if requests > 1 {
						code, body = 403, `{"message":"denied"}`
					}
				case "oversized":
					body = strings.Repeat("x", 400)
				case "no-etag":
					h.Del("ETag")
				case "no-store":
					h.Set("Cache-Control", "no-store")
				case "vary-star":
					h.Set("Vary", "*")
				case "unsolicited-304":
					code, body = 304, ""
				}
				if mode != "error" && r.Header.Get("If-None-Match") != "" {
					t.Error("uncacheable response sent a validator")
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			for range 2 {
				method := "GET"
				if mode == "write" {
					method = "POST"
				}
				req, err := http.NewRequest(method, "https://api.test/items", nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				if mode == "unsolicited-304" {
					if err == nil {
						t.Fatal("unsolicited 304 succeeded")
					}
					continue
				}
				must(t, err)
				body, err := io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
				if mode == "error" && requests == 2 && (res.StatusCode != 403 || strings.Contains(string(body), "original")) {
					t.Fatal("error replayed stale authorization")
				}
				if mode == "oversized" && len(body) != 400 {
					t.Fatal("oversized response was truncated")
				}
			}
			if requests != 2 {
				t.Fatal("GET skipped server revalidation or write was cached")
			}
		})
	}
}

func TestConditionalGETConcurrentReaders(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	_, err := f.driver.PullRequest(ctx, testRepo, 1)
	must(t, err)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			pr, err := f.driver.PullRequest(ctx, testRepo, 1)
			if err != nil || !reviewTrust(pr) {
				t.Errorf("concurrent conditional read failed: %+v %v", pr, err)
			}
		})
	}
	wg.Wait()
	if f.permissionCalls["writer"] != 21 || f.notModified != 60 {
		t.Fatalf("permission checks were skipped: permission calls=%d 304s=%d", f.permissionCalls["writer"], f.notModified)
	}
}

func TestPermissionHTTPCallsDoNotSerializeUsers(t *testing.T) {
	// Each request waits for both to enter the transport, exposing any permission
	// mutex held over network I/O. Use a transport so the fixture mutex cannot hide it.
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	f := newAPI(t)
	_, err := f.app.token(context.Background(), 7)
	must(t, err)
	tr := f.driver.client.Client().Transport.(*transport)
	tr.base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"permission":"write"}`)), Request: r}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, user := range []string{"one", "two"} {
		wg.Go(func() {
			allowed, err := f.driver.CanWrite(ctx, testRepo, user)
			if err != nil || !allowed {
				t.Errorf("concurrent permission read failed: %v %v", allowed, err)
			}
		})
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			t.Fatal("permissions held a lock across HTTP")
		}
	}
	close(release)
	wg.Wait()
}

func TestAPIFixtureUsesGitHubStatusAndErrorShapes(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	// Unknown installations must never mint a usable credential.
	_, err := f.app.token(ctx, 999)
	if err == nil {
		t.Fatal("unknown installation was accepted")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/api/v3/repos/acme/project/pulls/999", nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer secret-installation-1")
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	body, err := io.ReadAll(res.Body)
	must(t, err)
	must(t, res.Body.Close())
	if res.StatusCode != 404 || res.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(body), `"documentation_url"`) || res.Header.Get("X-RateLimit-Remaining") == "" {
		t.Fatalf("not a GitHub-shaped error: %d %s", res.StatusCode, body)
	}
	// Every empty collection uses [] rather than null.
	for _, path := range []string{"commits/empty/check-runs", "commits/empty/check-suites", "commits/empty/statuses", "issues/1/comments", "pulls?state=open&base=absent"} {
		if strings.Contains(path, "check-runs") {
			path += "?filter=all"
		}
		req, err := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/api/v3/repos/acme/project/"+path, nil)
		must(t, err)
		req.Header.Set("Authorization", "Bearer secret-installation-1")
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		body, err := io.ReadAll(res.Body)
		must(t, err)
		must(t, res.Body.Close())
		if strings.Contains(string(body), "null") || !strings.Contains(string(body), "[]") || res.Header.Get("ETag") == "" {
			t.Fatalf("invalid list JSON: %s", body)
		}
	}
	// The low-level SDK must observe 201 for all creates, not just parse a 200 body.
	token, created, err := f.app.client.Apps.CreateInstallationToken(ctx, 7, nil)
	must(t, err)
	if token.GetToken() == "" || created.StatusCode != 201 {
		t.Fatal("installation token create did not return 201")
	}
	_, created, err = f.driver.client.Checks.CreateCheckRun(ctx, "acme", "project", gh.CreateCheckRunOptions{Name: "CI", HeadSHA: "sha", ExternalID: new("external"), Status: new("in_progress"), Output: &gh.CheckRunOutput{Title: new("CI"), Summary: new("Running")}})
	must(t, err)
	if created.StatusCode != 201 {
		t.Fatal("check create did not return 201")
	}
	_, created, err = f.driver.client.Issues.CreateComment(ctx, "acme", "project", 1, gh.IssueCommentRequest{Body: "body"})
	must(t, err)
	if created.StatusCode != 201 {
		t.Fatal("comment create did not return 201")
	}
}
