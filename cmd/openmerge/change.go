package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func runChange(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "brief" {
		_, _ = fmt.Fprintln(stderr, "Usage: openmerge change brief <id> --repo owner/name [--base main] [--audience reviewer|operator|agent] [--json]")
		return 2
	}
	id := args[1]
	flags := flag.NewFlagSet("change brief", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", envDefault("OPENMERGE_API_URL", "http://127.0.0.1:8081"), "status API origin")
	repo := flags.String("repo", "", "owner/name (required)")
	base := flags.String("base", "main", "base branch")
	audience := flags.String("audience", "reviewer", "reviewer, operator or agent")
	asJSON := flags.Bool("json", false, "write public JSON response unchanged")
	tokenFile := flags.String("api-token-file", os.Getenv("OPENMERGE_API_TOKEN_FILE"), "read bearer token file")
	if err := flags.Parse(args[2:]); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	u, err := url.Parse(*api)
	parts := strings.Split(*repo, "/")
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		_, _ = fmt.Fprintln(stderr, "--api requires an http(s) origin without credentials, path, query or fragment")
		return 2
	}
	if flags.NArg() != 0 || queue.ChangeID(id).Validate() != nil || !brief.ValidAudience(*audience) || len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." || strings.ContainsAny(*repo, " \t\r\n?#%") || *base == "" || *base == "." || *base == ".." {
		_, _ = fmt.Fprintln(stderr, "brief requires a canonical change ID, --repo owner/name, nonempty --base and valid --audience")
		return 2
	}
	token, err := status.ReadToken(os.Getenv("OPENMERGE_API_TOKEN"), *tokenFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	path := "/v1/queues/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(*base) + "/changes/" + url.PathEscape(id) + "/brief?audience=" + url.QueryEscape(*audience)
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(*api, "/")+path, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid API URL")
		return 2
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "read status API:", err)
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return queueAPIError(response, token, stderr)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, brief.MaxViewBytes+1))
	var view brief.View
	if err != nil || len(raw) > brief.MaxViewBytes || json.Unmarshal(raw, &view) != nil || view.Version != 1 || view.Audience != *audience || view.ChangeID != id {
		_, _ = fmt.Fprintln(stderr, "invalid, oversized or unsupported brief API response")
		return 1
	}
	if !*asJSON {
		raw, err = brief.Render(view)
		raw = append(raw, '\n')
	}
	if err == nil {
		_, err = stdout.Write(raw)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "write brief:", err)
		return 1
	}
	return 0
}
