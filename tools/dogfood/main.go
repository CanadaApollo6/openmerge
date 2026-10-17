// Command dogfood measures ADR 0009 journal cohorts and independently audits
// queue landings. It is a separate tool, never part of the openmerge binary.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/google/go-github/v93/github"
)

type options struct {
	database, installation, repository, base, lane string
	json                                           bool
}

func (o *options) flags(fs *flag.FlagSet) {
	fs.StringVar(&o.database, "database-url", "", "PostgreSQL connection string (required; all queries READ ONLY)")
	fs.StringVar(&o.installation, "installation", "", "journal installation ID (required)")
	fs.StringVar(&o.repository, "repository", "CanadaApollo6/openmerge", "GitHub owner/repo")
	fs.StringVar(&o.base, "base", "main", "journal base branch")
	fs.StringVar(&o.lane, "lane", "", "exact journal lane (default empty)")
	fs.BoolVar(&o.json, "json", false, "emit JSON only")
}
func (o options) journal(ctx context.Context) (store.Journal, error) {
	if o.database == "" || o.installation == "" {
		return store.Journal{}, fmt.Errorf("--database-url and --installation are required")
	}
	r, err := store.OpenJournalReader(ctx, o.database)
	if err != nil {
		return store.Journal{}, err
	}
	defer r.Close()
	key := store.QueueKey{Installation: o.installation, Repository: o.repository, BaseBranch: o.base, Lane: o.lane}
	journals, err := r.Journals(ctx, []store.QueueKey{key})
	if err != nil {
		return store.Journal{}, err
	}
	if len(journals) != 1 {
		return store.Journal{}, fmt.Errorf("no journal for %+v; identifiers are exact and case-sensitive", key)
	}
	return journals[0], nil
}
func actionsClient(ctx context.Context, repository string) (Actions, error) {
	owner, repo, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return nil, fmt.Errorf("repository must be owner/repo")
	}
	token := strings.TrimSpace(os.Getenv("GH_TOKEN"))
	if token == "" {
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		if err != nil {
			return nil, fmt.Errorf("set GH_TOKEN or log in with gh auth login")
		}
		token = strings.TrimSpace(string(out))
	}
	client, err := github.NewClient(github.WithAuthToken(token))
	if err != nil {
		return nil, err
	}
	return githubActions{client: client, owner: owner, repo: repo}, nil
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: trailing JSON data", path)
	}
	return nil
}
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func reportCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	var o options
	o.flags(fs)
	from, to := fs.String("from", "", "ConfigApplied event ID, commit SHA, or RFC3339 time"), fs.String("to", "", "exclusive admission-window close (RFC3339 time or journal boundary)")
	auditFile := fs.String("audit", "", "JSON audit covering the entire period (otherwise comparison is inconclusive)")
	ci := fs.String("ci-config", "", "operator-recorded CI configuration identity (use the same value for A and B)")
	reviewed := fs.Bool("log-reviewed", false, "confirm no abort or material CI/server change in the period, from the operator's log")
	aborted := fs.String("aborted-reason", "", "operator-recorded abort (incorrect landing, hour-long wedge, or unexplained failure)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected report arguments")
	}
	j, err := o.journal(ctx)
	if err != nil {
		return err
	}
	start, err := boundary(j, *from)
	if err != nil {
		return err
	}
	// Resolve timestamp starts back to the exact ConfigApplied event.
	if start.EventID == 0 {
		var matches []Boundary
		for _, rec := range j.Events {
			if e, ok := rec.Event.(queue.ConfigApplied); ok && e.Validate() == nil && rec.RecordedAt.Equal(start.Time) {
				matches = append(matches, Boundary{Time: rec.RecordedAt, EventID: rec.ID, Commit: e.Commit})
			}
		}
		if len(matches) != 1 {
			return fmt.Errorf("--from must identify exactly one ConfigApplied; prefer event:<id>")
		}
		start = matches[0]
	}
	end, err := boundary(j, *to)
	if err != nil {
		return err
	}
	if end.Time.After(time.Now()) {
		return fmt.Errorf("--to is in the future; use a completed period")
	}
	r, err := reportJournal(j, Window{From: start, To: end})
	if err != nil {
		return err
	}
	r.CIConfig, r.LogReviewed, r.AbortedReason = *ci, *reviewed, *aborted
	a, err := actionsClient(ctx, o.repository)
	if err != nil {
		return err
	}
	r.Runs, err = countRuns(ctx, a, r.Queue.BaseBranch, r.Batches)
	if err != nil {
		return err
	}
	finishDrain(&r, j)
	if *auditFile != "" {
		var audit AuditResult
		if err := readJSON(*auditFile, &audit); err != nil {
			return err
		}
		if err := attachAudit(&r, j, audit); err != nil {
			return err
		}
	}
	if o.json {
		err = writeJSON(out, r)
	} else {
		err = printReport(out, r)
	}
	if err != nil {
		return err
	}
	if !r.DrainComplete {
		return fmt.Errorf("drain incomplete; report is provisional and cannot pass comparison")
	}
	return nil
}
func printReport(out io.Writer, r Report) error {
	var b []byte
	b = fmt.Appendf(b, "%s %s: %s -> %s (exclusive)\n", r.Queue.Repository, r.Queue.BaseBranch, r.Window.From.Time.Format(time.RFC3339Nano), r.Window.To.Time.Format(time.RFC3339Nano))
	b = fmt.Appendf(b, "Landed: %d; landed from tested position >=2: %d\nTime to merge (nearest rank): median %.1fs; p95 %.1fs\nFrom first enqueue: median %.1fs; p95 %.1fs\n", r.Landed, r.Engaged, r.TimeToMerge.Median, r.TimeToMerge.P95, r.FirstEnqueue.Median, r.FirstEnqueue.P95)
	b = fmt.Appendf(b, "Dequeued: %d; pending cohort: %d; landed at admission close: %d\nPosition >=3 landings: %d\nDrain complete: %t; end: %s; duration: %.1fs; held arrivals (proxy): %d\n", r.Dequeued, r.Pending, r.LandedAtClose, r.DeepEngaged, r.DrainComplete, r.DrainEnd.Format(time.RFC3339Nano), r.DrainSeconds, len(r.HeldArrivals))
	for _, warning := range r.Warnings {
		b = fmt.Appendf(b, "WARNING: %s\n", warning)
	}
	b = fmt.Appendf(b, "Traffic: %d accepted arrivals; %.3f/day\nCI run attempts: %d; runs/merge: %.3f\nMultiple batches: %.1fs / %.1fs non-idle (%.2f%%)\n", len(r.Admissions), r.ArrivalsPerDay, r.RunAttempts, r.RunsPerMerge, r.MultipleBatchSeconds, r.NonIdleSeconds, 100*r.MultipleBatchShare)
	if r.Landed == 0 {
		b = fmt.Appendf(b, "%s\n", "No landed cohort: percentiles and runs/merge are undefined (JSON scalar values are zero).")
	}
	if r.Audit == nil {
		b = fmt.Appendf(b, "%s\n", "Incorrect landings: UNKNOWN (attach --audit; comparison will be inconclusive)")
	} else {
		b = fmt.Appendf(b, "Audit: %d landing events checked; %d violations\n", len(r.Audit.CheckedEventIDs), len(r.Audit.Violations))
	}
	for _, c := range r.Changes {
		b = fmt.Appendf(b, "  #%d admission %d, batch %s position %d: %.1fs (first enqueue %.1fs)\n", c.PR, c.AdmissionID, c.BatchID, c.Position, c.Seconds, c.FirstSeconds)
	}
	for _, a := range r.Admissions {
		b = fmt.Appendf(b, "  Admission #%d at %s: queue length %d (including admission)\n", a.PR, a.AcceptedAt.Format(time.RFC3339Nano), a.QueueLength)
	}
	for _, p := range r.WindowHistory {
		b = fmt.Appendf(b, "  %s event %d: W=%d, max_depth=%d, open batches=%d\n", p.At.Format(time.RFC3339Nano), p.EventID, p.W, p.MaxDepth, p.OpenBatches)
	}
	for _, h := range r.Halvings {
		b = fmt.Appendf(b, "  Halving event %d: %d -> %d (%s, %s)\n", h.EventID, h.Before, h.After, h.Failure.BatchID, h.Failure.Reason)
	}
	_, err := out.Write(b)
	return err
}
func auditCommand(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	var o options
	o.flags(fs)
	since := fs.String("since", "", "inclusive RFC3339 time (required)")
	until := fs.String("until", time.Now().UTC().Format(time.RFC3339Nano), "exclusive RFC3339 time (default now)")
	repo := fs.String("git-repo", "", "existing complete clone or bare repository (no fetches)")
	cache := fs.String("git-cache", "", "server git-cache root; selects hashed owner/repo.git without modifying it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected audit arguments")
	}
	start, err := time.Parse(time.RFC3339Nano, *since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	end, err := time.Parse(time.RFC3339Nano, *until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}
	if end.After(time.Now()) {
		return fmt.Errorf("--until is in the future")
	}
	if (*repo == "") == (*cache == "") {
		return fmt.Errorf("provide exactly one of --git-repo or --git-cache")
	}
	if *cache != "" {
		*repo = filepath.Join(*cache, fmt.Sprintf("%x.git", sha256.Sum256([]byte(o.repository))))
	}
	j, err := o.journal(ctx)
	if err != nil {
		return err
	}
	a, err := actionsClient(ctx, o.repository)
	if err != nil {
		return err
	}
	result, err := audit(ctx, j, start, end, gitReader{dir: *repo}, a)
	if err != nil {
		return err
	}
	if o.json {
		if err := writeJSON(out, result); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(out, "Audited %d landing events on %s/%s: %d violations\n", len(result.CheckedEventIDs), o.repository, o.base, len(result.Violations)); err != nil {
			return err
		}
		for _, v := range result.Violations {
			if _, err := fmt.Fprintf(out, "event %d [%s]: %s\n", v.EventID, v.Check, v.Message); err != nil {
				return err
			}
		}
	}
	if len(result.Violations) > 0 {
		return fmt.Errorf("incorrect landing audit failed: %d violations; contain the queue and follow the dogfooding abort procedure", len(result.Violations))
	}
	return nil
}
func run(ctx context.Context, args []string, out, errs io.Writer) int {
	var err error
	if len(args) == 0 {
		err = fmt.Errorf("usage: dogfood report [flags] | audit [flags] | compare [--json] A.json B.json")
	} else {
		switch args[0] {
		case "report":
			err = reportCommand(ctx, args[1:], out)
		case "audit":
			err = auditCommand(ctx, args[1:], out)
		case "compare":
			fs := flag.NewFlagSet("compare", flag.ContinueOnError)
			asJSON := fs.Bool("json", false, "emit JSON")
			if err = fs.Parse(args[1:]); err == nil {
				if fs.NArg() != 2 {
					err = fmt.Errorf("compare needs A.json B.json")
				} else {
					var a, b Report
					if err = readJSON(fs.Arg(0), &a); err == nil {
						err = readJSON(fs.Arg(1), &b)
					}
					if err == nil {
						c := compare(a, b)
						if *asJSON {
							err = writeJSON(out, c)
						} else {
							text := c.Status + "\n- " + strings.Join(c.Reasons, "\n- ") + "\n"
							text += fmt.Sprintf("B latency with holdovers (%d landed): median %.1fs, p95 %.1fs; without (%d landed, %d holdovers removed): median %.1fs, p95 %.1fs\n", c.BLatency.WithCount, c.BLatency.With.Median, c.BLatency.With.P95, c.BLatency.WithoutCount, c.BLatency.HoldoverLanded, c.BLatency.Without.Median, c.BLatency.Without.P95)
							_, err = io.WriteString(out, text)
						}
						if err == nil {
							switch c.Status {
							case "pass":
								return 0
							case "fail":
								return 1
							case "aborted":
								return 3
							default:
								return 2
							}
						}
					}
				}
			}
		default:
			err = fmt.Errorf("unknown subcommand %q", args[0])
		}
	}
	if err != nil {
		_, _ = fmt.Fprintln(errs, err)
		return 1
	}
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
