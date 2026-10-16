package github

import (
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestConditionalGETRequiresOneValidEntityTag(t *testing.T) {
	for _, tc := range []struct {
		tags  []string
		valid bool
	}{
		{[]string{`"grant"`}, true}, {[]string{`W/"grant"`}, true}, {[]string{`""`}, true},
		{[]string{`"a,b\c"`}, true}, {[]string{" \tW/\"grant\"\t"}, true}, {[]string{"\"\x80\xff\""}, true},
		{nil, false}, {[]string{""}, false}, {[]string{"*"}, false},
		{[]string{`"grant", "revoked"`}, false}, {[]string{`"grant"`, `"revoked"`}, false},
		{[]string{"grant"}, false}, {[]string{`w/"grant"`}, false}, {[]string{`"a b"`}, false},
		{[]string{"\"a\tb\""}, false}, {[]string{"\"a\x7fb\""}, false}, {[]string{"\"a\nb\""}, false},
	} {
		t.Run(strings.Join(tc.tags, "/"), func(t *testing.T) {
			calls := 0
			tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				h := make(http.Header)
				h["Etag"] = tc.tags
				code, body := 200, "grant"
				if calls > 1 {
					conditional := r.Header.Get("If-None-Match")
					if (conditional != "") != tc.valid {
						t.Fatalf("conditional=%q valid=%v", conditional, tc.valid)
					}
					code, body = 200, "revoked"
					if tc.valid || conditional == "*" || strings.Contains(conditional, `"revoked"`) {
						code, body = 304, ""
					}
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))
			for i := range 2 {
				req, err := http.NewRequest("GET", "https://api.test/items", nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				must(t, err)
				body, err := io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
				if i == 1 && !tc.valid && string(body) != "revoked" {
					t.Fatalf("malformed validator retained a grant: %q", body)
				}
			}
		})
	}
}

func TestConditionalGETRejectsUnsafe304AndEvicts(t *testing.T) {
	for _, mode := range []string{"mismatch", "wildcard", "list", "multiple", "empty", "unquoted", "no-store", "vary-star"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				h := make(http.Header)
				h.Set("ETag", `"grant"`)
				code, body := 200, "grant"
				switch calls {
				case 2:
					if r.Header.Get("If-None-Match") != `"grant"` {
						t.Fatal("probe did not revalidate")
					}
					code, body = 304, ""
					switch mode {
					case "mismatch":
						h.Set("ETag", `"revoked"`)
					case "wildcard":
						h.Set("ETag", "*")
					case "list":
						h.Set("ETag", `"grant", "revoked"`)
					case "multiple":
						h.Add("ETag", `"grant"`)
					case "empty":
						h.Set("ETag", "")
					case "unquoted":
						h.Set("ETag", "grant")
					case "no-store":
						h.Add("Cache-Control", "private, max-age=60")
						h.Add("Cache-Control", "No-StOrE")
					case "vary-star":
						h.Add("Vary", "Accept")
						h.Add("Vary", "X-GitHub-Api-Version, *")
					}
				case 3:
					if r.Header.Get("If-None-Match") != "" {
						t.Fatal("unsafe 304 did not evict the grant")
					}
					body = "revoked"
				}
				return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}))
			for i := range 3 {
				req, err := http.NewRequest("GET", "https://api.test/items", nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				if i == 1 {
					if err == nil || res != nil {
						t.Fatal("unsafe 304 replayed a stale grant")
					}
					continue
				}
				must(t, err)
				body, err := io.ReadAll(res.Body)
				must(t, err)
				must(t, res.Body.Close())
				if i == 2 && string(body) != "revoked" {
					t.Fatalf("stale body after eviction: %q", body)
				}
			}
		})
	}
}

func TestConditionalGETPersists304Headers(t *testing.T) {
	calls := 0
	tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		h := make(http.Header)
		code, body := 200, "body"
		if calls == 1 {
			h.Set("ETag", `W/"v1"`)
			h.Set("Cache-Control", "private")
			h.Set("Link", "old")
		} else {
			code, body = 304, ""
			if r.Header.Get("If-None-Match") != `W/"v1"` {
				t.Fatal("lost stored validator")
			}
		}
		if calls == 2 {
			h.Set("ETag", `W/"v1"`)
			h.Set("Link", "new")
			h.Add("Cache-Control", "private")
			h.Add("Cache-Control", "no-cache")
			h.Set("Content-Length", "0")
		}
		return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	for i := range 3 {
		req, err := http.NewRequest("GET", "https://api.test/items", nil)
		must(t, err)
		res, err := tr.RoundTrip(req)
		must(t, err)
		body, err := io.ReadAll(res.Body)
		must(t, err)
		must(t, res.Body.Close())
		if string(body) != "body" || i > 0 && (res.Header.Get("Link") != "new" || res.ContentLength != 4 || !reflect.DeepEqual(res.Header.Values("Cache-Control"), []string{"private", "no-cache"})) {
			t.Fatalf("304 metadata was not retained: body=%q headers=%v", body, res.Header)
		}
	}
}

func TestConditionalGETHonorsEveryDirectiveLine(t *testing.T) {
	for _, mode := range []string{"vary", "cache-control"} {
		t.Run(mode, func(t *testing.T) {
			tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("If-None-Match") != "" {
					t.Fatal("directive on later header line was ignored")
				}
				h := make(http.Header)
				h.Set("ETag", `"v1"`)
				if mode == "vary" {
					h.Add("Vary", "Accept")
					h.Add("Vary", "X-GitHub-Api-Version, *")
				} else {
					h.Add("Cache-Control", "private")
					h.Add("Cache-Control", "max-age=60, NO-STORE")
				}
				return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("body")), Request: r}, nil
			}))
			for range 2 {
				req, err := http.NewRequest("GET", "https://api.test/items", nil)
				must(t, err)
				res, err := tr.RoundTrip(req)
				must(t, err)
				must(t, res.Body.Close())
			}
		})
	}
}
