package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const reportUsage = `Usage: openmerge report savings --installation <id> --repository owner/name --since <RFC3339> [options]

Summarizes the CI runs, CI minutes and time OpenMerge saved one queue in
[--since, --until), compared with a baseline of one full CI run per landed PR
plus the same reruns. Read-only: it opens a restricted journal reader (no
migrations, every query READ ONLY) and never contacts the forge.

Options:
  --database-url URL         PostgreSQL connection string (default OPENMERGE_DATABASE_URL)
  --installation ID          journal installation ID (required)
  --repository owner/name    exact journal repository spelling (required)
  --base BRANCH              journal base branch (default main)
  --lane LANE                exact journal lane (default empty)
  --since RFC3339            inclusive window start (required)
  --until RFC3339            exclusive window end (default now)
  --full-suite-minutes N     minutes for one full CI run; otherwise the observed
                             median journal wall-clock per run is used, labelled
  --timeout DURATION         journal read deadline (default 2m)
  --json                     emit JSON

See docs/savings-report.md for the baseline definition and how to read it.
Exit codes: 0 success; 2 usage; 1 read or computation failure.
`

func runReport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		if _, err := io.WriteString(stdout, reportUsage); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 || args[0] != "savings" {
		_, _ = io.WriteString(stderr, reportUsage)
		return 2
	}
	flags := flag.NewFlagSet("report savings", flag.ContinueOnError)
	// Flag errors can echo arguments, including a pasted connection string.
	flags.SetOutput(io.Discard)
	database := flags.String("database-url", os.Getenv("OPENMERGE_DATABASE_URL"), "")
	installation := flags.String("installation", "", "")
	repository := flags.String("repository", "", "")
	base := flags.String("base", "main", "")
	lane := flags.String("lane", "", "")
	since := flags.String("since", "", "")
	until := flags.String("until", "", "")
	minutes := flags.Float64("full-suite-minutes", 0, "")
	timeout := flags.Duration("timeout", store.DefaultJournalReadTimeout, "")
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(stdout, reportUsage); err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		_, _ = fmt.Fprintln(stderr, "report: invalid command flags; see report --help")
		return 2
	}
	now := time.Now().UTC()
	start, e1 := time.Parse(time.RFC3339Nano, *since)
	end, e2 := now, error(nil)
	if *until != "" {
		end, e2 = time.Parse(time.RFC3339Nano, *until)
	}
	switch {
	case flags.NArg() != 0 || *installation == "" || *base == "":
		_, _ = fmt.Fprintln(stderr, "report: --installation is required and arguments are flags only")
	case !auditRepo.MatchString(*repository):
		_, _ = fmt.Fprintln(stderr, "report: --repository must be owner/name")
	case e1 != nil || e2 != nil || !end.After(start):
		_, _ = fmt.Fprintln(stderr, "report: --since and --until must be RFC3339 with --since before --until")
	case end.After(now):
		_, _ = fmt.Fprintln(stderr, "report: --until is in the future; use a completed window")
	case *minutes < 0:
		_, _ = fmt.Fprintln(stderr, "report: --full-suite-minutes must not be negative")
	case *timeout < time.Millisecond:
		_, _ = fmt.Fprintln(stderr, "report: --timeout must be at least 1ms")
	case *database == "":
		_, _ = fmt.Fprintln(stderr, "report: set --database-url or OPENMERGE_DATABASE_URL")
	default:
		return reportSavings(*database, store.QueueKey{Installation: *installation, Repository: *repository, BaseBranch: *base, Lane: *lane},
			queuemetrics.SavingsWindow{Since: start, Until: end, FullSuiteMinutes: *minutes}, *timeout, *asJSON, stdout, stderr)
	}
	return 2
}

func reportSavings(database string, key store.QueueKey, w queuemetrics.SavingsWindow, timeout time.Duration, asJSON bool, stdout, stderr io.Writer) int {
	j, err := queuemetrics.ReadJournal(context.Background(), database, key, timeout)
	if err == nil {
		var s queuemetrics.Savings
		if s, err = queuemetrics.ComputeSavings(j, w); err == nil {
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				err = enc.Encode(s)
			} else {
				err = queuemetrics.WriteSavingsText(stdout, s)
			}
			if err == nil {
				return 0
			}
		}
	}
	secrets := []string{database}
	if u, parseErr := url.Parse(database); parseErr == nil {
		if password, ok := u.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	_, _ = fmt.Fprintln(stderr, "report:", diagnostic.Sanitize(err.Error(), secrets...))
	return 1
}
