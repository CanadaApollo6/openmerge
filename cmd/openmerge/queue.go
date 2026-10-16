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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func runQueue(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" {
		_, _ = fmt.Fprintln(stderr, "Usage: openmerge queue status [--json] [--api URL] [--repo owner/name] [--base main] [--pr N]")
		return 2
	}
	flags := flag.NewFlagSet("queue status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", envDefault("OPENMERGE_API_URL", "http://127.0.0.1:8081"), "status API origin (OPENMERGE_API_URL)")
	repo := flags.String("repo", "", "owner/name; omit to list all served queues")
	base := flags.String("base", "main", "base branch")
	pr := flags.Int("pr", 0, "PR number; requires --repo")
	asJSON := flags.Bool("json", false, "write the API response bytes unchanged")
	tokenFile := flags.String("api-token-file", os.Getenv("OPENMERGE_API_TOKEN_FILE"), "bearer token file (or OPENMERGE_API_TOKEN)")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	baseSet, prSet := false, false
	flags.Visit(func(f *flag.Flag) {
		baseSet = baseSet || f.Name == "base"
		prSet = prSet || f.Name == "pr"
	})
	u, err := url.Parse(*api)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		_, _ = fmt.Fprintln(stderr, "--api requires an http(s) origin without credentials, path, query or fragment")
		return 2
	}
	if flags.NArg() != 0 || *base == "" || *pr < 0 || prSet && *pr == 0 || *repo == "" && (prSet || baseSet) {
		_, _ = fmt.Fprintln(stderr, "status accepts flags only; --pr must be positive; --pr/--base require --repo")
		return 2
	}
	path := "/v1/queues"
	if *repo != "" {
		parts := strings.Split(*repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(*repo, " \t\r\n?#%") || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			_, _ = fmt.Fprintln(stderr, "--repo requires owner/name")
			return 2
		}
		path += "/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(*base)
		if *pr > 0 {
			path += "/prs/" + strconv.Itoa(*pr)
		}
	}
	token, err := status.ReadToken(os.Getenv("OPENMERGE_API_TOKEN"), *tokenFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
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
	const maxResponse = 32 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	var result status.Response
	if err != nil || len(body) > maxResponse || json.Unmarshal(body, &result) != nil || result.Version != status.Version || *repo == "" && result.Queues == nil || *repo != "" && result.Queue == nil || *pr > 0 && (result.PR == nil || result.Why == nil) {
		_, _ = fmt.Fprintln(stderr, "invalid, oversized or unsupported status API response")
		return 1
	}
	if *asJSON {
		_, err = stdout.Write(body)
	} else {
		err = printStatus(stdout, result)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "write status:", err)
		return 1
	}
	return 0
}

func queueAPIError(response *http.Response, token string, stderr io.Writer) int {
	const maxErrorBody = 8 << 10
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody+1))
	var failure struct {
		Version int    `json:"version"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	valid := err == nil && len(body) <= maxErrorBody && json.Unmarshal(body, &failure) == nil && failure.Version == status.Version && failure.Error != "" && failure.Message != ""
	if valid {
		_, _ = fmt.Fprintf(stderr, "status API: %s: %s (HTTP %d)\n", diagnostic.Sanitize(failure.Error, token), diagnostic.Sanitize(failure.Message, token), response.StatusCode)
	} else {
		_, _ = fmt.Fprintln(stderr, "status API returned HTTP", response.StatusCode)
	}
	switch response.StatusCode {
	case http.StatusNotFound:
		return 3
	case http.StatusUnauthorized, http.StatusForbidden:
		return 4
	case http.StatusConflict:
		if valid && failure.Error == "ambiguous-queue" {
			return 5
		}
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		if valid && (failure.Error == "invalid-request" || failure.Error == "request-too-large") {
			return 2
		}
	}
	return 1
}

func envDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func printStatus(out io.Writer, response status.Response) error {
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "REPO\tBASE\tPOS\tPR\tHEAD\tSTATUS\tREASON"); err != nil {
		return err
	}
	queues := response.Queues
	if response.Queue != nil {
		queues = []status.Queue{*response.Queue}
	}
	for _, q := range queues {
		var reasons []string
		for _, block := range q.Blocks {
			reasons = append(reasons, block.Reason)
		}
		pauseReason := q.PauseReason
		if len(reasons) > 0 {
			pauseReason = strings.Join(reasons, ", ")
		}
		entries := q.Entries
		if response.PR != nil {
			entries = []status.Entry{*response.PR}
		}
		if len(entries) == 0 {
			state := "idle"
			if q.Paused {
				state = "paused"
			}
			if _, err := fmt.Fprintf(table, "%s\t%s\t-\t-\t-\t%s\t%s\n", q.Repository, q.Base, state, pauseReason); err != nil {
				return err
			}
		}
		for _, entry := range entries {
			if entry.Status == "paused" {
				entry.Reason = pauseReason
			}
			head := entry.HeadSHA[:min(12, len(entry.HeadSHA))]
			if _, err := fmt.Fprintf(table, "%s\t%s\t%d\t#%d\t%s\t%s\t%s\n", q.Repository, q.Base, entry.Position, entry.PR, head, entry.Status, entry.Reason); err != nil {
				return err
			}
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if response.Why != nil {
		if _, err := fmt.Fprintln(out, response.Why.Summary); err != nil {
			return err
		}
		for _, event := range response.Why.Events {
			if _, err := fmt.Fprintf(out, "  %d  %s  %s  %s\n", event.ID, event.At.Format(time.RFC3339), event.Type, event.Summary); err != nil {
				return err
			}
		}
	}
	return nil
}
