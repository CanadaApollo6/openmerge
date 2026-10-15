<img src="assets/icon.svg" width="64" height="64" alt="">

# OpenMerge

**An open-source merge queue and pull-request automation engine for the age of coding agents.**

Agents now open pull requests faster than CI and humans can land them. The merge path — queuing, batching, speculative testing, flaky-test handling, and the rules around them — has become the bottleneck, and the best tools for it are closed, paywalled, or tied to a single forge. OpenMerge brings the ideas that Mergify popularized to everyone, under the Apache 2.0 license.

> **Status: pre-alpha.** We're in Phase 0: research and design. There is no runnable code yet. See the [roadmap](ROADMAP.md).

## What it will do

- **Merge queue that never lies:** what was tested is exactly what lands.
- **Throughput:** batching with bisection, speculative chains with an adaptive window, priorities, multiple queues, two-step CI.
- **Monorepo lanes:** independent scopes merge in parallel (path globs, Bazel, Nx, Turborepo, Pants).
- **Flaky-test handling:** ingest JUnit/CTRF, score flakiness, quarantine, and never evict a PR for a known flake.
- **Agent-native:** JSON API, CLI with `--json`, an MCP server, change identity across re-pushes, per-agent budgets, and machine-readable failure reasons.
- **PR automation:** labels, reviews, backports, merge protections, stacks.
- **Runs anywhere:** one binary plus Postgres. GitHub first, then GitLab and Forgejo/Gitea.

## Docs

- [Roadmap](ROADMAP.md): principles, architecture, phases, and open decisions
- [Research](docs/research/README.md): how Mergify works, and the wider merge-queue landscape

## Contributing

The project is just starting, and the most useful contributions right now are design feedback: open an issue or comment on the open decisions in the roadmap.

## License

[Apache License 2.0](LICENSE). OpenMerge is an independent project and is not affiliated with or endorsed by Mergify SAS. See [NOTICE](NOTICE).
