# Research

Background research that informs the [roadmap](../../ROADMAP.md). Compiled on 2026-10-15 from public documentation, changelogs, GitHub repositories, papers, and blog posts; each report ends with its sources. Claims the authors could not confirm against a primary source are marked "unverified".

| Report | Covers |
|---|---|
| [Mergify merge queue](mergify-merge-queue.md) | Config format, queue lifecycle, draft-PR batches, speculative checks, batching and bisection, two-step CI, priorities, scopes, freeze/pause, condition grammar, GitHub integration, the 2022 open-source engine's architecture |
| [Mergify platform](mergify-platform.md) | Workflow automation actions, commands, merge protections, stacks, CI Insights and Test Engine, agent features, API, pricing, license history, user pain points |
| [Landscape and prior art](landscape.md) | GitHub merge queue, GitLab merge trains, bors-ng / rust-lang/bors / Homu, Zuul, Prow/Tide, Uber SubmitQueue, Trunk, Aviator, Graphite, PR bots, Gerrit and LUCI, Forgejo/Gitea queues, flaky-test tooling, algorithm tradeoffs |
| [Phase 2 dogfooding](phase2-dogfood.md) | Plan, metrics, abort conditions and log for running this repository's queue at `max_depth: 4` before the default changes |
| [CI efficiency lab](ci-efficiency-lab.md) | Simulator benchmark of CI-cost strategies (tree dedupe, risk-weighted batching, fail-fast, Bayesian culprit isolation) on synthetic workloads; runner-minutes, waste and latency with 95% CIs |

These are snapshots. Mergify and the wider ecosystem change quickly; check the cited sources before relying on a detail.
