package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

// runAuthorityCompletion reads the repository-wide completion signal. Exit 0
// only when the server reports complete for exactly the requested intent; an
// incomplete answer, any API failure or a mismatched echo exits nonzero, so a
// script cannot mistake HTTP 200 or a per-queue receipt for completion.
func runAuthorityCompletion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("admin authority-completion", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", envDefault("OPENMERGE_API_URL", "http://127.0.0.1:8081"), "status API origin (OPENMERGE_API_URL)")
	tokenFile := flags.String("api-token-file", os.Getenv("OPENMERGE_API_TOKEN_FILE"), "bearer token file (or OPENMERGE_API_TOKEN)")
	installation := flags.String("installation", "", "GitHub App installation ID")
	repo := flags.String("repo", "", "owner/name")
	digest := flags.String("digest", "", "intended common effective digest")
	targets := flags.String("targets", "", `JSON object of base to expected effective digest, e.g. {"release/v1":"<digest>"}`)
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	request := store.AuthorityCompletionRequest{Installation: *installation, Repository: *repo, Digest: *digest}
	targetsValid := *targets == "" || json.Unmarshal([]byte(*targets), &request.Targets) == nil
	if flags.NArg() != 0 || !targetsValid || request.Validate() != nil {
		_, _ = fmt.Fprintln(stderr, "authority-completion requires --installation, --repo and --digest and/or --targets with valid expected digests")
		return 2
	}
	u, err := url.Parse(*api)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		_, _ = fmt.Fprintln(stderr, "--api requires an http(s) origin without credentials, path, query or fragment")
		return 2
	}
	token, err := status.ReadToken(os.Getenv("OPENMERGE_API_TOKEN"), *tokenFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	query := url.Values{"installation": {request.Installation}, "repo": {request.Repository}}
	if request.Digest != "" {
		query.Set("digest", request.Digest)
	}
	if len(request.Targets) > 0 {
		raw, _ := json.Marshal(request.Targets)
		query.Set("targets", string(raw))
	}
	httpRequest, err := http.NewRequest(http.MethodGet, strings.TrimRight(*api, "/")+"/v1/authority-completion?"+query.Encode(), nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid API URL")
		return 2
	}
	httpRequest.Header.Set("Accept", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: statusClientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(httpRequest)
	if err != nil {
		// Transport errors embed the request URL; report only the class.
		if os.IsTimeout(err) {
			_, _ = fmt.Fprintf(stderr, "status API request timed out (client limit %s); retry\n", statusClientTimeout)
		} else {
			_, _ = fmt.Fprintln(stderr, "status API unavailable; retry")
		}
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return queueAPIError(response, token, stderr)
	}
	const maxResponse = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	var result store.AuthorityCompletion
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err != nil || len(body) > maxResponse || decoder.Decode(&result) != nil || !sameCompletionRequest(result.Request, request) {
		_, _ = fmt.Fprintln(stderr, "invalid, oversized or mismatched authority completion response")
		return 1
	}
	if _, err := stdout.Write(body); err != nil {
		return 1
	}
	if err := result.RequireComplete(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func sameCompletionRequest(got, want store.AuthorityCompletionRequest) bool {
	if len(got.Targets) == 0 && len(want.Targets) == 0 {
		got.Targets, want.Targets = nil, nil
	}
	return reflect.DeepEqual(got, want)
}
