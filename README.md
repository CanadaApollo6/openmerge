<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** Phase 1 is in progress: the planner, configuration, forge contract, and simulator are in place; storage, the GitHub driver, and the server are next. It cannot run against a real repository yet. See the [roadmap](ROADMAP.md).

## What it will do

- **Merge queue that never lies:** what was tested is exactly what lands.
- **Throughput:** batching with bisection, speculative chains with an adaptive window, priorities, multiple queues, two-step CI.
- **Monorepo lanes:** independent scopes merge in parallel (path globs, Bazel, Nx, Turborepo, Pants).
- **Flaky-test handling:** ingest JUnit/CTRF, score flakiness, quarantine, and never evict a PR for a known flake.
- **Agent-native:** JSON API, CLI with `--json`, an MCP server, change identity across re-pushes, per-agent budgets, and machine-readable failure reasons.
- **PR automation:** labels, reviews, backports, merge protections, stacks.
- **Runs anywhere:** one binary plus Postgres. GitHub first, then GitLab and Forgejo/Gitea.

## Docs

- [Roadmap](ROADMAP.md): principles, architecture, phases, and decisions
- [Architecture decisions](docs/adr/README.md): the ADRs behind the design
- [Research](docs/research/README.md): how Mergify works, and the wider merge-queue landscape

## Try the simulator

The simulator runs the real planner and executor against an in-memory forge on a virtual clock, and checks after every step that main only ever contains tested, passing commits.

```bash
go run ./cmd/openmerge sim --seed 42            # a 500-PR day: 5% broken changes, 2-minute CI
go run ./cmd/openmerge sim --faults --json      # add per-method errors, lost acknowledgements, late/duplicate pushes
```

Flags: `--prs`, `--broken-rate`, `--flake-rate`, `--ci`, `--ci-max`, `--seed`, `--faults`, `--drop-webhooks`, `--merge-method`, `--window`, `--repush-rate`, `--conflict-rate`, `--json`. `--ci-max` uses `--ci` as the minimum of a duration range. JSON reports the full effective configuration and mean, median, and p95 merge times in seconds. See the [`internal/sim` package docs](internal/sim) for the full model.

## Contributing

The project is just starting, and design feedback is the most useful contribution right now. Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and the DCO sign-off, and [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).
