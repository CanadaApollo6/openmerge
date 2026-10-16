# OpenMerge roadmap

OpenMerge is an Apache-2.0 merge queue and pull-request automation engine: the ideas Mergify popularized, open to everyone, built for a world where coding agents open more pull requests than humans can shepherd.

This roadmap is grounded in the research in [`docs/research/`](docs/research/README.md). Read those first if you want the why behind any decision here.

## Why this exists

- **CI and the merge path are now the bottleneck.** Agents produce PRs at 10–1000x human rates, re-push often, and fail CI more. A serial queue at 30-minute CI tops out near 48 merges a day. Anthropic reports 30,000+ merges a day internally and dropped classic pre-merge queuing to get there.
- **The good tools are closed or gated.** GitHub's native merge queue is unavailable for private repos outside Enterprise Cloud and gives no control over batching, bisection, lanes, or priorities. GitLab merge trains are Premium-only. Mergify's engine went private in 2022, and on 2026-10-08 its CLI and test plugins moved to a proprietary license that forbids competing use. Trunk, Aviator, and Graphite are closed SaaS.
- **The open-source options are fragmented.** bors-ng is archived. rust-lang/bors is excellent but serial and GitHub-only. Zuul is powerful but heavy to run. Prow/Tide is tied to Kubernetes' job model. Nothing open combines speculative batching, monorepo lanes, flaky-test handling, and agent-native interfaces.

## Product principles

1. **What you tested is what you merged.** Land the exact tested SHA (or the exact tested tree, for squash and rebase) or don't land.
2. **Agent-native, human-friendly.** Every state change and every failure reason is machine-readable (JSON API, CLI `--json`, MCP). Humans get the same facts rendered as one quiet check run.
3. **Quiet by default.** No PR or comment spam. One check run per PR, comments only on outcomes the author must act on.
4. **Self-hostable in five minutes.** One binary plus Postgres. No Redis, no Kubernetes required. A hosted offering is optional and never gets features first.
5. **Forge-portable by design.** The engine never calls GitHub directly. It talks to a narrow forge driver interface that GitHub, GitLab, Forgejo/Gitea, and a fake forge all implement.
6. **Outsiders can run the tests.** The full engine runs against an in-memory fake forge, so a contributor can test end to end without GitHub credentials. (Mergify cites untestable code as a reason it closed its engine.)
7. **Stable config.** Versioned schema, no silent breaking changes, and migrations shipped as code.

## Architecture (proposed)

```
            webhooks ─┐                 ┌─> forge driver (GitHub | GitLab | Forgejo | fake)
                      v                 │
  forge ──> ingest ──> event log ──> planner ──> executor ──> reporter (checks, comments)
            ^  (reconciler polls)    (pure)    (idempotent)
            │                           │
            └── CI results ─────────────┘
                                         └─> API / CLI / MCP / webhooks / OpenTelemetry
```

- **Ingest:** verifies and normalizes forge webhooks into events. A **reconciler** re-reads forge state periodically (Tide-style search queries) so a dropped webhook can never wedge the queue.
- **Event log and state:** Postgres. Queue state is derived from an append-only event log, so every decision is replayable and debuggable. Jobs use `SELECT … FOR UPDATE SKIP LOCKED`; one queue (repo × base branch × lane) is processed by one worker at a time.
- **Planner:** a pure function from (config, queue state, CI results, clock) to a list of intended actions. This is where batching, speculation, bisection, and priorities live. Being pure makes it unit- and simulation-testable without a forge.
- **Executor:** applies planned actions through the forge driver. Every action is idempotent and keyed, so retries and crashes converge.
- **Test refs, not draft PRs (default):** speculative batches are pushed to bot-owned refs such as `openmerge/queue/<base>/<batch-id>`, and CI triggers on pushes to that prefix. Draft-PR mode exists as a fallback for CI systems that only test PRs, and a `mergify/merge-queue/` prefix compatibility mode keeps existing two-step-CI filters working during migration.
- **Landing:** one atomic git push that moves the base to the exact tested commit, leased on both the expected base SHA and a protected fence branch, so a moved base or an abandoned attempt can never land ([ADR 0004](docs/adr/0004-forge-driver-interface.md)). This needs OpenMerge to be allowed to push to the protected branch; teams that cannot allow it can use the GitHub native merge queue adapter in Phase 5.

## Phases

Each phase has an exit test. A phase is done when its exit test passes on the fake forge and on a real GitHub test org.

### Phase 0 — Foundations (now)

- [x] Apache-2.0 license, NOTICE, public repo
- [x] Research: Mergify merge queue, Mergify platform, prior art and landscape
- [x] [ADR 0001](docs/adr/0001-implementation-language.md): implement in Go
- [x] [ADR 0002](docs/adr/0002-storage-and-state-model.md): Postgres only, event-sourced queue state, pure planner
- [x] [ADR 0003](docs/adr/0003-configuration-format.md): `.openmerge.yml`, condition grammar, Mergify importer
- [x] [ADR 0004](docs/adr/0004-forge-driver-interface.md): forge driver interface and fake forge contract
- [x] CONTRIBUTING, code of conduct, governance, security policy, DCO sign-off, issue and PR templates
- [x] Go scaffold with `make check` and CI for the repo itself
- [ ] Project site and a public design discussion

**Exit:** ADRs merged; a contributor can clone, run `make test`, and see a green (empty) suite.

### Phase 1 — MVP: a queue that never lies (in progress)

- GitHub App (webhook intake, reconciler, installation tokens, rate-limit budget)
- Fake forge with a scripted CI simulator; a simulation test suite for the planner
- Config loader for `.openmerge.yml` with JSON Schema; condition engine covering the core attributes (`base`, `label`, `author`, `#approved-reviews-by`, `check-success/failure/pending`, `files`, `draft`, `conflict`) and `and`/`or`/`not`
- Branch protection and ruleset injection as conditions
- Queue: serial mode, then **batching with n-ary bisection**; `queue_conditions` vs `merge_conditions` (two-step CI)
- Test refs plus exact-SHA landing; merge methods: merge, squash, rebase, fast-forward
- One "OpenMerge Queue" check run per PR; dequeue reasons as a stable enum
- Commands: `@openmerge queue | dequeue | refresh` with permission checks
- REST API and CLI (`openmerge queue status --json`) from day one
- Single binary + Postgres; `docker compose up` quickstart

Implemented so far: the planner and simulator, durable Postgres workers, GitHub
App setup and polling/webhook driver, runtime ruleset validation, batching and
bisection, exact-tested landing, commands, read-only API/CLI, and the Compose
quickstart. Phase 1 is **not complete**. Remaining follow-ups include automated
classic branch protection checks, validation of installed App permission grants,
and runtime metadata reads for label/file conditions (also head, title and
conflict). Operators still audit classic protection and App permissions.
The exit test also requires verification on a real GitHub test organization.

**Exit:** OpenMerge merges its own PRs. A simulated 500-PR day with 5% failure rate lands every passing PR with main green throughout.

### Phase 2 — Throughput

- Speculative chain of batches with a **Zuul-style AIMD window** (grows on success, halves on failure)
- Dynamic batch sizing from observed failure rate
- **Optimistic failure resolution** (a failed batch waits for its successors; a later pass that contains it clears a flake)
- Priorities (`priority_rules`, numeric levels, optional preemption) and multiple queues
- Check timeouts (automatic, p95-based) and bounded CI retries
- Pause, scheduled freezes with exclusions, `schedule` conditions
- Read-only web dashboard: queue graph, batch history, why-is-my-PR-stuck explainer
- **MCP server** exposing queue status, enqueue/dequeue, failure explanations

**Exit:** at 2,000 simulated PRs/day with 30-minute CI, median time-to-merge stays under 2x CI duration, and CI runs per merged PR stays under 1.5.

### Phase 3 — Agent-native

- **Change identity:** a queue entry tracks a logical change across re-pushes and rebases; a superseding push cancels stale runs instead of stacking new ones
- Per-author and per-agent concurrency, CI-spend, and retry budgets, with escalation to a human
- Agent-aware policy rules (policy-bot style): e.g. agent-authored PRs touching `infra/**` need two human approvals
- Structured failure explanations: which batch, which check, which tests, flaky or real, suggested next action
- Outbound webhooks and OpenTelemetry export for every queue event
- Agent skills and docs written for agents (`llms.txt`, Markdown docs, stable `--json` and exit codes)

**Exit:** an unattended coding agent can take a PR from "opened" to "merged or definitively rejected" using only the CLI or MCP, with no human reading logs.

### Phase 4 — Monorepos and CI intelligence

- **Scopes and lanes:** path-glob scopes first, then adapters for Bazel, Nx, Turborepo, and Pants; independent lanes form a DAG, not one line
- Barrier files (lockfiles, toolchains) that serialize against everything
- Direct merge for PRs behind base only by merges in disjoint scopes
- **Flaky-test service:** JUnit XML and CTRF ingest, per-test flakiness score, quarantine list exposed to CI, broken-vs-flaky classification, auto-release on recovery
- Queue decisions consume flake data: a failure only in quarantined tests is pending, not an eviction
- CI analytics: runs per merged PR, wasted runs and why, cost by lane and author

**Exit:** on a monorepo with 10 independent scopes, throughput scales near-linearly with scope count, and a known-flaky test never evicts a PR.

### Phase 5 — Workflow automation and portability

- `pull_request_rules` actions: label, comment, assign, request_reviews (with random weighting), review, update/rebase, close, backport, copy
- Merge protections: `Depends-On:` (cross-repo), `Merge-After:`, config-change validation
- Stacks: queue a stack as one unit; land bottom-up
- **Forge drivers:** GitLab (bringing batching and bisection to GitLab Free/CE), Forgejo/Gitea (required-status + native auto-merge pattern), GitHub Enterprise Server
- **GitHub native merge queue adapter:** for teams that want GitHub's UX, OpenMerge drives `enqueuePullRequest` and acts as a smart required check on `merge_group`
- **Mergify config importer** (`openmerge config import .mergify.yml`) with a compatibility report

**Exit:** the same `.openmerge.yml` runs unchanged against GitHub, GitLab, and Forgejo test instances.

### Phase 6 — Scale, prediction, 1.0

- Predictive scheduling: spend the CI budget on speculations with the highest expected value (pass probability and build-time prediction, after Uber's SubmitQueue and follow-ups)
- **Post-merge verification + auto-revert mode** for teams at agent scale that drop pre-merge serialization
- High availability (multiple workers, leader-free queue ownership via row locks), horizontal scaling
- Security review, threat model, signed releases, SBOM
- Config schema 1.0 with a compatibility promise

**Exit:** 1.0 release; a public deployment handling at least 10,000 merges/day.

## Gating modes (the spectrum OpenMerge supports)

| Mode | Main is | Throughput | When |
|---|---|---|---|
| Strict serial | always green | lowest | small repos, regulated code |
| Batched + speculative (default) | always green | high | most teams |
| Optimistic | green except rare intermittent bugs | higher | high flake rates |
| Lanes (scopes) | green per lane | scales with independence | monorepos |
| Post-merge verify + auto-revert | briefly red, auto-healed | highest | agent-scale monorepos |

## Decisions

Phase 0 settled the founding decisions:

1. **Language:** Go ([ADR 0001](docs/adr/0001-implementation-language.md)).
2. **Config compatibility:** native `.openmerge.yml` plus a Mergify importer; OpenMerge never reads `.mergify.yml` at runtime ([ADR 0003](docs/adr/0003-configuration-format.md)).
3. **Command prefix and branding:** `@openmerge` comment commands and the `openmerge/` ref prefix.
4. **Governance:** project lead now, a documented path to a steering committee, DCO instead of a CLA ([GOVERNANCE.md](GOVERNANCE.md)).

Still open: whether to run a free hosted instance for open-source projects, and who operates it. That decision can wait until Phase 2.

## Reuse and licensing rules

- We may study and adapt Apache-2.0 and MIT code with attribution: rust-lang/bors, Prow/Tide, Zuul, bulldozer, policy-bot, luci-go, bors-ng, Homu, and the 2022 Apache-2.0 snapshot of Mergify's engine.
- We do not copy AGPL code (Kodiak, Mergeable) or anything from proprietary products. Learn from public docs only, and write our own docs.
- "Mergify" is a trademark of Mergify SAS. OpenMerge is independent and not affiliated; we use the name only to describe compatibility and prior art.
