package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/cidoctor"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const reportUsage = `Usage: openmerge report savings --installation <id> --repository owner/name --since <RFC3339> [options]

       openmerge report ci-doctor --installation <id> --repository owner/name --since <RFC3339> --actions-file <path> [options]

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
  --actions-file PATH        recorded Actions JSON, required for ci-doctor (no live reader)
  --full-suite-minutes N     savings only: minutes for one full CI run; otherwise the observed
                             median journal wall-clock per run is used, labelled
  --timeout DURATION         journal read deadline (default 2m)
  --json                     emit JSON

CI doctor reports advisory findings, sample counts, gaps, and step classification.
See docs/savings-report.md and docs/ci-doctor.md for definitions and contracts.
Exit codes: 0 success; 2 usage; 1 read or computation failure.
`

func runReport(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		if _, err := io.WriteString(stdout, reportUsage); err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 || (args[0] != "savings" && args[0] != "ci-doctor") {
		_, _ = io.WriteString(stderr, reportUsage)
		return 2
	}
	flags := flag.NewFlagSet("report "+args[0], flag.ContinueOnError)
	// Flag errors can echo arguments, including a pasted connection string.
	flags.SetOutput(io.Discard)
	database := flags.String("database-url", os.Getenv("OPENMERGE_DATABASE_URL"), "")
	installation := flags.String("installation", "", "")
	repository := flags.String("repository", "", "")
	base := flags.String("base", "main", "")
	lane := flags.String("lane", "", "")
	since := flags.String("since", "", "")
	until := flags.String("until", "", "")
	var minutes float64
	var actionsFile string
	if args[0] == "savings" {
		flags.Float64Var(&minutes, "full-suite-minutes", 0, "")
	} else {
		flags.StringVar(&actionsFile, "actions-file", "", "")
	}
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
	case minutes < 0:
		_, _ = fmt.Fprintln(stderr, "report: --full-suite-minutes must not be negative")
	case *timeout < time.Millisecond:
		_, _ = fmt.Fprintln(stderr, "report: --timeout must be at least 1ms")
	case args[0] == "ci-doctor" && actionsFile == "":
		_, _ = fmt.Fprintln(stderr, "report: --actions-file is required for ci-doctor")
	case *database == "":
		_, _ = fmt.Fprintln(stderr, "report: set --database-url or OPENMERGE_DATABASE_URL")
	default:
		key := store.QueueKey{Installation: *installation, Repository: *repository, BaseBranch: *base, Lane: *lane}
		if args[0] == "ci-doctor" {
			return reportCIDoctor(*database, key, cidoctor.Window{Since: start, Until: end}, actionsFile, *timeout, *asJSON, stdout, stderr)
		}
		return reportSavings(*database, key,
			queuemetrics.SavingsWindow{Since: start, Until: end, FullSuiteMinutes: minutes}, *timeout, *asJSON, stdout, stderr)
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
	return reportError(database, err, stderr)
}

func reportError(database string, err error, stderr io.Writer) int {
	secrets := []string{database}
	if u, parseErr := url.Parse(database); parseErr == nil {
		if password, ok := u.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	_, _ = fmt.Fprintln(stderr, "report:", diagnostic.Sanitize(err.Error(), secrets...))
	return 1
}

func reportCIDoctor(database string, key store.QueueKey, w cidoctor.Window, path string, timeout time.Duration, asJSON bool, stdout, stderr io.Writer) int {
	actions, err := readActionsFile(path)
	if err != nil {
		return reportError(database, err, stderr)
	}
	j, err := queuemetrics.ReadJournal(context.Background(), database, key, timeout)
	if err != nil {
		return reportError(database, err, stderr)
	}
	r, err := cidoctor.Compute(j, actions, w)
	if err == nil {
		if asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(r)
		} else {
			err = cidoctor.WriteText(stdout, r)
		}
	}
	if err != nil {
		return reportError(database, err, stderr)
	}
	return 0
}

func readActionsFile(path string) (cidoctor.Actions, error) {
	f, err := os.Open(path)
	if err != nil {
		return cidoctor.Actions{}, errors.New("cannot open recorded Actions file")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil || len(data) > 64<<20 {
		return cidoctor.Actions{}, errors.New("cannot read recorded Actions file (maximum 64 MiB)")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var actions cidoctor.Actions
	if err = dec.Decode(&actions); err != nil {
		return actions, errors.New("invalid recorded Actions JSON")
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return actions, errors.New("recorded Actions file must contain one JSON object (maximum 64 MiB)")
	}
	return actions, nil
}
