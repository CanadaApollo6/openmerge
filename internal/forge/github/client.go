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
	base        http.RoundTripper
	token       func(context.Context) (string, error)
	origin      *url.URL
	now         func() time.Time
	mu          sync.Mutex
	until       time.Time
	secondary   bool
	remaining   int
	reset       time.Time
	budgetKnown bool
	limit       int
	inflight    int
	spent       [6]int
	strikes     uint
	cache       *responseCache
	appIdentity bool
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.origin.Scheme || req.URL.Host != t.origin.Host || !strings.HasPrefix(req.URL.Path, t.origin.Path) {
		return nil, errors.New("github refused credentials outside API origin")
	}
	ticket, err := t.reserve(req.Context())
	if err != nil {
		return nil, err
	}
	defer func() { t.mu.Lock(); t.inflight--; t.mu.Unlock() }()
	token, err := t.token(req.Context())
	if err != nil {
		return nil, safeError(err)
	}
	copyReq := req.Clone(req.Context())
	copyReq.Header.Set("Authorization", "Bearer "+token)
	notModified := false
	res, err := t.conditionalGET(copyReq, token, func() { notModified = true })
	if err != nil {
		return nil, safeError(err)
	}
	if notModified && ticket.charged {
		t.mu.Lock()
		if ticket.window.Equal(t.reset) {
			t.remaining++
			t.spent[ticket.class] = max(0, t.spent[ticket.class]-1)
		}
		t.mu.Unlock()
	}
	if remaining, err := strconv.Atoi(res.Header.Get("X-RateLimit-Remaining")); err == nil {
		if reset, err := strconv.ParseInt(res.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			t.mu.Lock()
			resetAt := time.Unix(reset, 0)
			if !t.budgetKnown || resetAt.After(t.reset) {
				t.remaining, t.reset, t.budgetKnown = max(0, remaining-t.inflight+1), resetAt, true
				t.spent = [6]int{}
			} else if resetAt.Equal(t.reset) {
				t.remaining = min(t.remaining, max(0, remaining-t.inflight+1))
			}
			if limit, err := strconv.Atoi(res.Header.Get("X-RateLimit-Limit")); err == nil && limit > 0 {
				t.limit = limit
			}
			t.mu.Unlock()
		}
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

// BackoffUntil reserves settlement capacity for legacy callers and honors
// transport cooldowns. Missing rate headers never invent a budget.
func (d *Driver) BackoffUntil() time.Time {
	t, ok := d.client.Client().Transport.(*transport)
	if !ok {
		return time.Time{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	until := t.until
	if t.budgetKnown && t.remaining < max(100, t.settlementReserve()) && t.reset.After(t.now()) {
		until = maxTime(until, t.reset)
	}
	return until
}

// RetryAfter exposes quota deadlines without coupling workers to the adapter.
func (e *RateLimitError) RetryAfter() time.Time { return e.RetryAt }
