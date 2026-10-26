package github

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestTransportNilBody(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, code := range []int{http.StatusOK, http.StatusNotModified, http.StatusForbidden, http.StatusTooManyRequests} {
			t.Run(fmt.Sprintf("cache=%t/status=%d", cached, code), func(t *testing.T) {
				requests := 0
				tr := cacheTransport(t, 4, 4096, roundTripFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					header := make(http.Header)
					header.Set("ETag", `"empty"`)
					status := code
					if requests == 2 {
						if r.Header.Get("If-None-Match") != `"empty"` {
							t.Error("empty response was not cached")
						}
						status = http.StatusNotModified
					}
					// Deliberately violate RoundTripper's non-nil Body contract.
					return &http.Response{StatusCode: status, Header: header, Request: r}, nil
				}))
				if !cached {
					tr.cache = nil
				}
				count := 1
				if cached && code == http.StatusOK {
					count = 2 // Revalidate the cached empty representation, also with a nil body.
				}
				for range count {
					req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.test/items", nil)
					must(t, err)
					res, err := tr.RoundTrip(req)
					if cached && code == http.StatusNotModified {
						if err == nil || res != nil {
							t.Fatalf("uncached 304: response=%v error=%v", res, err)
						}
						continue
					}
					if code == http.StatusTooManyRequests {
						var rate *RateLimitError
						if !errors.As(err, &rate) || !rate.Secondary || res != nil {
							t.Fatalf("429: response=%v error=%v", res, err)
						}
						continue
					}
					must(t, err)
					if res.StatusCode != code || res.Body == nil {
						t.Fatalf("response: status=%d body=%v", res.StatusCode, res.Body)
					}
					body, err := io.ReadAll(res.Body)
					must(t, err)
					must(t, res.Body.Close())
					if len(body) != 0 {
						t.Fatalf("body=%q", body)
					}
				}
			})
		}
	}
}
