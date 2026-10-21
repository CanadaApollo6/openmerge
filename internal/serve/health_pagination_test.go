package serve

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	ghdriver "github.com/CanadaApollo6/openmerge/internal/forge/github"
)

type paginationTransport func(*http.Request) (*http.Response, error)

func (f paginationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type stalledPageBody struct{ ctx context.Context }

func (b stalledPageBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b stalledPageBody) Close() error             { return nil }

type paginatedHealthDriver struct {
	forge.Driver
	pages *ghdriver.Driver
	after func(error)
}

func (d paginatedHealthDriver) ListOpenPullRequests(ctx context.Context, repo forge.RepoRef, base string) ([]forge.PullRequestSummary, error) {
	result, err := d.pages.ListOpenPullRequests(ctx, repo, base)
	if d.after != nil {
		d.after(err)
	}
	return result, err
}

// Exercise the real worker loop, GitHub pagination, HTTP transport and health API
// together. Fake HTTP completes in virtual time; no network or GitHub is used.
func TestHealthGitHubPagination(t *testing.T) {
	// Initialize the process-wide resolver outside virtual-time bubbles. Each
	// database pool and its WaitGroups must then stay inside their own bubble.
	_, err := net.LookupHost("localhost")
	must(t, err)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	for _, mode := range []string{"healthy", "revalidated", "hung", "streaming", "failed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, w, _, _ := fixture(t, repositoryConfig)
				// Channels used by the virtual-time loop must belong to its bubble.
				w.wake = make(chan struct{}, 1)
				var completed atomic.Int64
				failureChecked := false
				priming := mode == "revalidated"
				var revalidated atomic.Int64
				tr := paginationTransport(func(r *http.Request) (*http.Response, error) {
					body := "[]"
					header := make(http.Header)
					code := 200
					switch r.URL.Path {
					case "/app":
						body = `{"id":42,"slug":"queue-app"}`
					case "/app/installations/7/access_tokens":
						body = fmt.Sprintf(`{"token":"fixture","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
					default:
						if !strings.HasSuffix(r.URL.Path, "/pulls") {
							return nil, fmt.Errorf("unexpected request: %s", r.URL.Path)
						}
						if completed.Load() == 2 && mode == "hung" {
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						if completed.Load() == 2 && mode == "streaming" {
							return &http.Response{StatusCode: 200, Header: header, Body: stalledPageBody{r.Context()}, Request: r}, nil
						}
						if !priming {
							time.Sleep(30 * time.Second)
						}
						if completed.Load() == 2 && mode == "failed" {
							code = 500
							body = `{"message":"fixture failure"}`
						} else {
							completed.Add(1)
						}
						// Enough pages to keep this single call running for ten minutes.
						if mode != "healthy" && mode != "revalidated" || completed.Load() < 20 {
							header.Set("Link", fmt.Sprintf(`<https://github.test/repos/owner/repo/pulls?page=%d>; rel="next"`, completed.Load()+1))
						}
						header.Set("ETag", `"page"`)
						if mode == "revalidated" && !priming {
							if r.Header.Get("If-None-Match") != `"page"` {
								t.Fatal("page was not primed")
							}
							code, body = http.StatusNotModified, ""
							revalidated.Add(1)
						}
					}
					return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				app, err := ghdriver.New(ghdriver.Config{AppID: 42, PrivateKey: privateKey, APIBaseURL: "https://github.test/", HTTPClient: &http.Client{Transport: tr, Timeout: time.Hour}})
				must(t, err)
				pages, err := app.Installation(t.Context(), 7)
				must(t, err)
				if priming {
					_, err = pages.ListOpenPullRequests(t.Context(), w.repo(), "main")
					must(t, err)
					completed.Store(0)
					priming = false
				}
				w.Driver = paginatedHealthDriver{Driver: w.Driver, pages: pages, after: func(err error) {
					if mode == "failed" && err != nil && !failureChecked {
						failureChecked = true
						if got := w.health.Load().LastProgressAt; got == nil || !got.Equal(time.Now().Add(-30*time.Second)) {
							t.Errorf("failed page advanced progress: %v at %v", got, time.Now())
						}
					}
				}}
				s.options.Health.ProgressStallLimit = 4 * time.Minute
				s.options.Health.MaxSuccessAge = time.Hour
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); s.loop(ctx, w) }()
				defer func() { cancel(); <-done }()
				synctest.Wait()
				for range 2 {
					time.Sleep(30 * time.Second)
					synctest.Wait()
					assertHealthCode(t, s, 200)
				}
				last := *w.health.Load().LastProgressAt
				if completed.Load() != 2 || !last.Equal(time.Now()) {
					t.Fatalf("completed=%d last=%v now=%v", completed.Load(), last, time.Now())
				}
				switch mode {
				case "healthy", "revalidated":
					for range 36 {
						time.Sleep(15 * time.Second)
						synctest.Wait()
						assertHealthCode(t, s, 200)
					}
					if mode == "revalidated" && revalidated.Load() != 20 {
						t.Fatalf("wire 304s=%d want=20", revalidated.Load())
					}
					if completed.Load() != 20 {
						t.Fatalf("pages=%d", completed.Load())
					}
				case "hung", "streaming":
					time.Sleep(4*time.Minute + time.Second)
					synctest.Wait()
					assertHealthCode(t, s, 503)
					if !w.health.Load().LastProgressAt.Equal(last) {
						t.Fatal("hung page refreshed progress")
					}
				case "failed":
					// The failed request completes before the cycle's durable work/turn can
					// refresh progress. Assert at the HTTP boundary in the separate driver test.
					time.Sleep(30 * time.Second)
					synctest.Wait()
					if completed.Load() != 2 {
						t.Fatal("failed page counted as successful")
					}
				}
			})
		})
	}
}

func TestHealthProgressHookOutsideCycle(t *testing.T) {
	_, w, _, _ := fixture(t, repositoryConfig)
	ctx := forge.WithProgressHook(t.Context(), func() { w.publishProgress(time.Now()) })
	before := w.health.Load()
	forge.NotifyProgress(ctx)
	if w.health.Load() != before {
		t.Fatal("progress outside a cycle changed health")
	}
}
