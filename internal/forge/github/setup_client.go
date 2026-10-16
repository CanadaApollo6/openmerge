package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

type setupAPI struct {
	base *url.URL
	http *http.Client
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
	return &setupAPI{base: origin, http: client}, nil
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
		return false, &APIError{StatusCode: res.StatusCode}
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 || json.Unmarshal(data, result) != nil {
		return false, errors.New("invalid GitHub setup response")
	}
	return strings.Contains(res.Header.Get("Link"), `rel="next"`), nil
}

func splitRepoPath(repo string) (string, string, error) {
	return splitRepo(forge.RepoRef{Path: repo})
}
