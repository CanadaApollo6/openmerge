package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type setupAPI struct {
	base    *url.URL
	http    *http.Client
	secrets []string
}

func setupClient(api, token string) (*setupAPI, error) {
	origin, err := baseURL(api)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if token != "" {
		client.Transport = &transport{base: http.DefaultTransport, token: func(context.Context) (string, error) { return token, nil }, origin: origin, now: time.Now, cache: newResponseCache(64, 4<<20)}
	}
	return &setupAPI{base: origin, http: client, secrets: []string{token}}, nil
}

func (c *setupAPI) request(ctx context.Context, method, path string, body, result any) error {
	_, err := c.page(ctx, method, path, body, result)
	return err
}

func (c *setupAPI) page(ctx context.Context, method, path string, body, result any) (bool, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return false, errors.New("cannot encode GitHub setup request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, bytes.NewReader(data))
	if err != nil {
		return false, errors.New("cannot create GitHub setup request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return false, safeError(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false, c.responseError(res)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 || json.Unmarshal(data, result) != nil {
		return false, errors.New("invalid GitHub setup response")
	}
	return strings.Contains(res.Header.Get("Link"), `rel="next"`), nil
}

// Setup diagnostics expose only selected, bounded fields, never raw response
// bodies, request URLs, reflected credentials, or validation error values.
type setupAPIError struct {
	status           *APIError
	message          string
	details          []setupErrorDetail
	documentationURL string
}

type setupErrorDetail struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func (e *setupAPIError) Unwrap() error { return e.status }

func (e *setupAPIError) Error() string {
	var out strings.Builder
	out.WriteString(e.status.Error())
	if e.message != "" {
		fmt.Fprintf(&out, ": %s", e.message)
	}
	for _, detail := range e.details {
		fmt.Fprintf(&out, "; error: resource=%q field=%q code=%q message=%q", detail.Resource, detail.Field, detail.Code, detail.Message)
	}
	if e.documentationURL != "" {
		fmt.Fprintf(&out, "; documentation_url=%s", e.documentationURL)
	}
	return out.String()
}

var setupTokenPattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+)\b|(?i:\b(?:bearer|basic)\s+\S+)`)

func (c *setupAPI) sanitize(value string) string {
	for _, secret := range c.secrets {
		if secret != "" {
			for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				value = strings.ReplaceAll(value, encoded, "[REDACTED]")
			}
		}
	}
	value = setupTokenPattern.ReplaceAllString(value, "[REDACTED]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 1024 {
		return string(runes[:1024]) + "…"
	}
	return string(runes)
}

func (c *setupAPI) responseError(res *http.Response) error {
	status := &APIError{StatusCode: res.StatusCode}
	data, err := io.ReadAll(io.LimitReader(res.Body, (64<<10)+1))
	var body struct {
		Message          string            `json:"message"`
		Errors           []json.RawMessage `json:"errors"`
		DocumentationURL string            `json:"documentation_url"`
	}
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &body) != nil {
		return status
	}
	diagnostic := &setupAPIError{status: status, message: c.sanitize(body.Message)}
	for _, raw := range body.Errors[:min(len(body.Errors), 10)] {
		var detail setupErrorDetail
		var message string
		if json.Unmarshal(raw, &message) == nil {
			detail.Message = message
		} else if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		detail.Resource, detail.Field = c.sanitize(detail.Resource), c.sanitize(detail.Field)
		detail.Code, detail.Message = c.sanitize(detail.Code), c.sanitize(detail.Message)
		diagnostic.details = append(diagnostic.details, detail)
	}
	// Only GitHub documentation links are useful here; drop credentials, query
	// strings and fragments, which can contain echoed request secrets.
	if u, err := url.Parse(body.DocumentationURL); err == nil && u.Scheme == "https" && u.Host == "docs.github.com" && u.User == nil {
		u.RawQuery, u.Fragment = "", ""
		if safe := c.sanitize(u.String()); safe == u.String() {
			diagnostic.documentationURL = safe
		}
	}
	return diagnostic
}

func splitRepoPath(repo string) (string, string, error) {
	return splitRepo(forge.RepoRef{Path: repo})
}
