package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/sim"
)

func runSim(args []string, stdout, stderr io.Writer) int {
	cfg := sim.DefaultConfig()
	flags := flag.NewFlagSet("sim", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&cfg.PRs, "prs", cfg.PRs, "number of trusted PRs (plus extra untrusted fork probes)")
	flags.IntVar(&cfg.Planner.BatchMaxSize, "batch-size", cfg.Planner.BatchMaxSize, "maximum PRs per batch (0 defaults to 1; max 128)")
	flags.DurationVar(&cfg.Planner.BatchMaxWait, "batch-wait", cfg.Planner.BatchMaxWait, "maximum wait to form a partial batch")
	flags.Float64Var(&cfg.BrokenRate, "broken-rate", cfg.BrokenRate, "fraction of PRs with broken changes")
	flags.Float64Var(&cfg.FlakeRate, "flake-rate", cfg.FlakeRate, "chance that a non-broken test commit fails CI")
	flags.DurationVar(&cfg.CIDuration, "ci", cfg.CIDuration, "duration of each CI run")
	flags.DurationVar(&cfg.CIMax, "ci-max", 0, "maximum CI duration (uses --ci as the minimum)")
	flags.Float64Var(&cfg.DropWebhookRate, "drop-webhooks", cfg.DropWebhookRate, "fraction of CI webhooks dropped")
	flags.DurationVar(&cfg.ArrivalWindow, "window", cfg.ArrivalWindow, "PR arrival window")
	flags.Float64Var(&cfg.RepushRate, "repush-rate", cfg.RepushRate, "fraction of queued PRs receiving a new head")
	flags.Float64Var(&cfg.ConflictRate, "conflict-rate", cfg.ConflictRate, "fraction of adjacent PR pairs sharing a changed file")
	flags.Float64Var(&cfg.ExternalPushRate, "external-push-rate", cfg.ExternalPushRate, "fraction of PRs accompanied by external main pushes during testing and before landing")
	method := flags.String("merge-method", string(queue.Merge), "merge, squash, rebase, or fast-forward")
	flags.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "deterministic random seed")
	faults := flags.Bool("faults", false, "inject transient forge failures and landing crashes")
	asJSON := flags.Bool("json", false, "print result as JSON (durations in seconds)")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(flags.Output(), "Usage: openmerge sim [--prs N] [--broken-rate R] [--flake-rate R] [--ci DURATION] [--seed S] [--faults] [--json]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "sim does not accept positional arguments")
		return 2
	}
	if cfg.CIDuration <= 0 {
		_, _ = fmt.Fprintln(stderr, "CI duration must be positive")
		return 2
	}
	cfg.MergeMethod = queue.MergeMethod(*method)
	if cfg.CIMax != 0 {
		cfg.CIMin = cfg.CIDuration
	}
	if *faults {
		cfg.Faults = sim.DefaultFaults()
	}
	result, err := sim.Run(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return 1
		}
		return 0
	}
	dequeued := 0
	for _, count := range result.Dequeued {
		dequeued += count
	}
	_, err = fmt.Fprintf(stdout,
		"Merged %d/%d PRs; dequeued %d; queued %d; blocked %d untrusted forks\n"+
			"Time to merge: mean %s, median %s, p95 %s; CI runs %d (%.2f/merged PR)\n"+
			"Simulated %s in %d steps; faults recovered %d/%d\n",
		result.Merged, result.PRs, dequeued, result.StillQueued, result.Blocked["untrusted"],
		result.MeanTimeToMerge.Round(time.Second), result.MedianTimeToMerge.Round(time.Second), result.P95TimeToMerge.Round(time.Second), result.CIRuns, result.CIRunsPerMergedPR,
		result.SimulatedSpan.Round(time.Minute), result.Steps, result.FaultsRecovered, result.FaultsInjected)
	if err != nil {
		return 1
	}
	return 0
}
