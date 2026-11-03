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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/cidoctor"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/queuemetrics"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

const reportUsage = `Usage: openmerge report savings --installation <id> --repository owner/name --since <RFC3339> [options]

       openmerge report ci-doctor --installation <id> --repository owner/name --since <RFC3339> (--actions-live | --actions-file <path>) [options]

Summarizes the CI runs, CI minutes and time OpenMerge saved one queue in
[--since, --until), compared with a baseline of one full CI run per landed PR
plus the same reruns. Read-only: it opens a restricted journal reader (no
migrations, every query READ ONLY). CI doctor can also read live Actions metadata.

Options:
  --database-url URL         PostgreSQL connection string (default OPENMERGE_DATABASE_URL)
  --installation ID          journal installation ID (required)
  --repository owner/name    exact journal repository spelling (required)
  --base BRANCH              journal base branch (default main)
  --lane LANE                exact journal lane (default empty)
  --since RFC3339            inclusive window start (required)
  --until RFC3339            exclusive window end (default now)
  --actions-file PATH        ci-doctor: recorded Actions JSON (maximum 64 MiB)
  --actions-live             ci-doctor: fetch live metadata; exclusive with --actions-file
  --actions-api-budget N     live data request cap, including retries (default 500)
  --config-dir DIR           live App credentials directory (app-id, private-key.pem)
  --app-id ID                live App ID; use with --private-key instead of --config-dir
  --private-key FILE         live App RSA PEM key path
  --full-suite-minutes N     savings only: minutes for one full CI run; otherwise the observed
                             median journal wall-clock per run is used, labelled
  --timeout DURATION         journal and live Actions deadline (default 2m)
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
	var actions actionsOptions
	actions.Budget = cidoctor.DefaultActionsAPIBudget
	if args[0] == "savings" {
		flags.Float64Var(&minutes, "full-suite-minutes", 0, "")
	} else {
		flags.StringVar(&actions.File, "actions-file", "", "")
		flags.BoolVar(&actions.Live, "actions-live", false, "")
		flags.IntVar(&actions.Budget, "actions-api-budget", cidoctor.DefaultActionsAPIBudget, "")
		flags.StringVar(&actions.ConfigDir, "config-dir", "", "")
		flags.Int64Var(&actions.AppID, "app-id", 0, "")
		flags.StringVar(&actions.PrivateKey, "private-key", "", "")
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
	case args[0] == "ci-doctor" && actions.Live == (actions.File != ""):
		_, _ = fmt.Fprintln(stderr, "report: choose exactly one of --actions-live or --actions-file")
	case args[0] == "ci-doctor" && actions.Budget <= 0:
		_, _ = fmt.Fprintln(stderr, "report: --actions-api-budget must be positive")
	case args[0] == "ci-doctor" && !actions.Live && (actions.ConfigDir != "" || actions.AppID != 0 || actions.PrivateKey != "" || actions.Budget != cidoctor.DefaultActionsAPIBudget):
		_, _ = fmt.Fprintln(stderr, "report: App credentials and --actions-api-budget require --actions-live")
	case actions.Live && (actions.ConfigDir != "" && (actions.AppID != 0 || actions.PrivateKey != "") || actions.ConfigDir == "" && (actions.AppID <= 0 || actions.PrivateKey == "")):
		_, _ = fmt.Fprintln(stderr, "report: live Actions requires --config-dir or positive --app-id with --private-key")
	case actions.Live && !positiveInstallation(*installation):
		_, _ = fmt.Fprintln(stderr, "report: live Actions requires a positive numeric --installation")
	case *database == "":
		_, _ = fmt.Fprintln(stderr, "report: set --database-url or OPENMERGE_DATABASE_URL")
	default:
		key := store.QueueKey{Installation: *installation, Repository: *repository, BaseBranch: *base, Lane: *lane}
		if args[0] == "ci-doctor" {
			return reportCIDoctor(*database, key, cidoctor.Window{Since: start, Until: end}, actions, *timeout, *asJSON, stdout, stderr)
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

func reportCIDoctor(database string, key store.QueueKey, w cidoctor.Window, opts actionsOptions, timeout time.Duration, asJSON bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var actions cidoctor.Actions
	var err error
	if !opts.Live {
		actions, err = readActionsFile(opts.File)
		if err != nil {
			return reportError(database, err, stderr)
		}
	}
	j, err := queuemetrics.ReadJournal(ctx, database, key, timeout)
	if err != nil {
		return reportError(database, err, stderr)
	}
	if opts.Live {
		var driver *github.Driver
		driver, err = opts.reader(ctx, key.Installation)
		if err == nil {
			actions, err = cidoctor.ReadActions(ctx, driver, j, w, opts.Budget)
		}
		if err != nil {
			return reportError(database, err, stderr)
		}
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

type actionsOptions struct {
	File, ConfigDir, PrivateKey string
	AppID                       int64
	Live                        bool
	Budget                      int
}

func positiveInstallation(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0
}

func (opts actionsOptions) reader(ctx context.Context, installation string) (*github.Driver, error) {
	if opts.ConfigDir != "" {
		data, err := os.ReadFile(filepath.Join(opts.ConfigDir, "app-id"))
		if err != nil {
			return nil, errors.New("CI doctor cannot read App ID from --config-dir")
		}
		opts.AppID, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil || opts.AppID <= 0 {
			return nil, errors.New("CI doctor requires a positive App ID in --config-dir")
		}
		opts.PrivateKey = filepath.Join(opts.ConfigDir, "private-key.pem")
	}
	key, err := os.ReadFile(opts.PrivateKey)
	if err != nil {
		return nil, errors.New("CI doctor cannot read App private key")
	}
	app, err := github.New(github.Config{AppID: opts.AppID, PrivateKey: key})
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(installation, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("CI doctor requires a positive installation ID")
	}
	return app.Installation(ctx, id)
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
