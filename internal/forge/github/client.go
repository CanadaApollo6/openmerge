package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"
)

// RateLimitError tells the caller when another request may be attempted. No
// request is sent during the cooldown; uncertain writes are never auto-retried.
type RateLimitError struct {
	RetryAt   time.Time
	Secondary bool
}

func (e *RateLimitError) Error() string {
	return "github rate limited until " + e.RetryAt.UTC().Format(time.RFC3339)
}

// APIError deliberately excludes response bodies, URLs, and wrapped transport
// diagnostics: GitHub or a proxy could echo credentials in any of those.
type APIError struct{ StatusCode int }

func (e *APIError) Error() string {
	return fmt.Sprintf("github API request failed (HTTP %d)", e.StatusCode)
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, cause) {
			return cause
		}
	}
	var rate *RateLimitError
	if errors.As(err, &rate) {
		return rate
	}
	var primary *gh.RateLimitError
	if errors.As(err, &primary) {
		return &RateLimitError{RetryAt: primary.Rate.Reset.Time}
	}
	var response *gh.ErrorResponse
	if errors.As(err, &response) && response.Response != nil {
		return &APIError{StatusCode: response.Response.StatusCode}
	}
	return errors.New("github API transport or response failed")
}

type transport struct {
	base      http.RoundTripper
	token     func(context.Context) (string, error)
	origin    *url.URL
	now       func() time.Time
	mu        sync.Mutex
	until     time.Time
	secondary bool
	strikes   uint
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.origin.Scheme || req.URL.Host != t.origin.Host || !strings.HasPrefix(req.URL.Path, t.origin.Path) {
		return nil, errors.New("github refused credentials outside API origin")
	}
	t.mu.Lock()
	if t.now().Before(t.until) {
		err := &RateLimitError{RetryAt: t.until, Secondary: t.secondary}
		t.mu.Unlock()
		return nil, err
	}
	t.mu.Unlock()
	token, err := t.token(req.Context())
	if err != nil {
		return nil, safeError(err)
	}
	copyReq := req.Clone(req.Context())
	copyReq.Header.Set("Authorization", "Bearer "+token)
	res, err := t.base.RoundTrip(copyReq)
	if err != nil {
		return nil, safeError(err)
	}
	exhausted := res.Header.Get("X-RateLimit-Remaining") == "0"
	limited := res.StatusCode == 429
	secondary := limited && !exhausted
	if res.StatusCode == 403 || res.StatusCode == 429 {
		prefix, readErr := io.ReadAll(io.LimitReader(res.Body, 8192))
		res.Body = &prefixBody{Reader: io.MultiReader(bytes.NewReader(prefix), res.Body), Closer: res.Body}
		if readErr != nil {
			_ = res.Body.Close()
			return nil, errors.New("github rate-limit response unreadable")
		}
		message := strings.ToLower(string(prefix))
		secondary = secondary || res.Header.Get("Retry-After") != "" && !exhausted || strings.Contains(message, "secondary rate limit") || strings.Contains(message, "abuse detection")
		limited = limited || secondary || exhausted
	}
	if limited || exhausted {
		t.mu.Lock()
		now := t.now()
		until := now.Add(time.Minute)
		if secondary {
			shift := min(t.strikes, 6)
			until = now.Add(time.Minute * time.Duration(1<<shift))
			t.strikes++
		}
		if raw := res.Header.Get("X-RateLimit-Reset"); exhausted {
			if unix, e := strconv.ParseInt(raw, 10, 64); e == nil {
				until = maxTime(until, time.Unix(unix, 0))
			}
		}
		if raw := res.Header.Get("Retry-After"); raw != "" {
			if seconds, e := strconv.ParseInt(raw, 10, 64); e == nil && seconds >= 0 && seconds <= 86400 {
				until = maxTime(until, now.Add(time.Duration(seconds)*time.Second))
			} else if date, e := http.ParseTime(raw); e == nil {
				until = maxTime(until, date)
			}
		}
		t.until = maxTime(t.until, until)
		t.secondary = secondary
		rate := &RateLimitError{RetryAt: t.until, Secondary: secondary}
		t.mu.Unlock()
		if limited {
			_ = res.Body.Close()
			return nil, rate
		}
	} else if res.StatusCode < 400 {
		t.mu.Lock()
		t.strikes = 0
		t.mu.Unlock()
	}
	return res, nil
}

type prefixBody struct {
	io.Reader
	io.Closer
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
