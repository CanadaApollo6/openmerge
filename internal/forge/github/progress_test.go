package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type progressErrorBody struct{ io.Reader }

func (b progressErrorBody) Read(p []byte) (int, error) {
	n, _ := b.Reader.Read(p)
	return n, io.ErrUnexpectedEOF
}
func (b progressErrorBody) Close() error { return nil }

func TestTransportProgressOnlyAfterSuccessfulBody(t *testing.T) {
	for _, mode := range []string{"success", "failed", "partial", "read-error", "transport-error", "no-hook"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			ctx := forge.WithProgressHook(context.Background(), func() { calls++ })
			if mode == "no-hook" {
				ctx = context.Background()
			}
			tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if mode == "transport-error" {
					return nil, errors.New("fixture transport failure")
				}
				code := 200
				if mode == "failed" {
					code = 500
				}
				body := io.NopCloser(strings.NewReader("[]"))
				if mode == "read-error" {
					body = progressErrorBody{strings.NewReader("[]")}
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: body, Request: r}, nil
			}))
			req, err := http.NewRequestWithContext(ctx, "GET", tr.origin.String()+"repos/o/r/pulls", nil)
			must(t, err)
			res, err := tr.RoundTrip(req)
			if calls != 0 {
				t.Fatal("headers counted as progress")
			}
			if mode == "transport-error" {
				if err == nil {
					t.Fatal("missing error")
				}
				return
			}
			must(t, err)
			if mode != "partial" {
				_, _ = io.ReadAll(res.Body)
			}
			must(t, res.Body.Close())
			want := 0
			if mode == "success" {
				want = 1
			}
			if calls != want {
				t.Fatalf("notifications=%d want=%d", calls, want)
			}
		})
	}
}

func TestTransportProgressUncached304(t *testing.T) {
	calls := 0
	tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("ETag", `"missing"`)
		return &http.Response{StatusCode: http.StatusNotModified, Header: h, Body: http.NoBody, Request: r}, nil
	}))
	ctx := forge.WithProgressHook(t.Context(), func() { calls++ })
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.test/items", nil)
	must(t, err)
	_, err = tr.RoundTrip(req)
	if err == nil || calls != 0 {
		t.Fatalf("uncached 304: error=%v progress=%d", err, calls)
	}
}

func TestTransportProgressNilBody(t *testing.T) {
	calls := 0
	tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Request: r}, nil
	}))
	ctx := forge.WithProgressHook(t.Context(), func() { calls++ })
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.test/items", nil)
	must(t, err)
	res, err := tr.RoundTrip(req)
	must(t, err)
	if res.Body != http.NoBody || calls != 1 {
		t.Fatalf("empty response: body=%v progress=%d", res.Body, calls)
	}
	_, err = io.ReadAll(res.Body)
	must(t, err)
	must(t, res.Body.Close())
	if calls != 1 {
		t.Fatalf("empty response published twice: %d", calls)
	}
}
