# OpenMerge roadmap

OpenMerge is an Apache-2.0 merge queue and pull-request automation engine: the ideas Mergify popularized, open to everyone, built for a world where coding agents open more pull requests than humans can shepherd.

This roadmap is grounded in the research in [`docs/research/`](docs/research/README.md). Read those first if you want the why behind any decision here.

## Why this exists

- **CI and the merge path are now the bottleneck.** Agents produce PRs at 10–1000x human rates, re-push often, and fail CI more. A serial queue at 30-minute CI tops out near 48 merges a day. Anthropic reports 30,000+ merges a day internally and dropped classic pre-merge queuing to get there ([Redesigning source control at Anthropic](https://www.anthropic.com/engineering/redesigning-source-control)).
- **The good tools are closed or gated.** GitHub's native merge queue is unavailable for private repos outside Enterprise Cloud and gives no control over batching, bisection, lanes, or priorities. GitLab merge trains are Premium-only. Mergify's engine went private in 2022, and on 2026-10-08 its CLI and test plugins moved to a proprietary license that forbids competing use. Trunk, Aviator, and Graphite are closed SaaS.
- **The open-source options are fragmented.** bors-ng is archived. rust-lang/bors is excellent but serial and GitHub-only. Zuul is powerful but heavy to run. Prow/Tide is tied to Kubernetes' job model. Nothing open combines speculative batching, monorepo lanes, flaky-test handling, and agent-native interfaces.

## Product principles

1. **Never lie about what was tested.** Strict queues land the exact tested commit or nothing. Post-merge queues say exactly what was tested before landing, keep a green ref that only passing code reaches, and never present an untested commit as tested ([ADR 0005](docs/adr/0005-verification-modes.md)).
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

Each phase has an exit test. A phase is done when its exit test passes on the fake forge and on a real GitHub repository.

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

### Phase 1 — MVP: a queue that never lies (complete)

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
App setup and polling/webhook driver, runtime ruleset/classic-protection and
installed permission-grant validation, all core condition observations, batching and
bisection, exact-tested landing, commands, read-only API/CLI, and the Compose
quickstart. Labels and changed files are observed from the PR, file pagination
is bounded at 1,000 paths (larger PRs remain unknown), and uncomputed conflict
state remains unknown. Definite protection or landing-permission failures pause
through the durable rules block; unreadable protection warns and retains state.
Classic reads use installation permissions, so operators still audit protection
when Administration read is unavailable and hidden ruleset bypass ownership.

- [x] Publish the [version-1 JSON Schema](schema/openmerge.v1.json), loader agreement tests, and `openmerge config schema`.
- [x] Fast-forward landing, including linear-chain batches and divergence dequeue.
- [x] Exit test on a real repository: since 2026-10-16 OpenMerge has landed its own
  PRs on CanadaApollo6/openmerge (#17, #19 to #23 and the PR that closed this phase),
  each as exactly the CI-tested commit. The 500-PR simulation runs in CI.

Phase 1 is **complete**. The Mergify importer remains a Phase 5 deliverable.

**Exit:** OpenMerge merges its own PRs. A simulated 500-PR day with 5% failure rate lands every passing PR with main green throughout.

### Phase 2.1 — Serve every installed repository (complete)

ADR 0006 lands in four PRs:

- [x] PR 1: additive version-1 multi-queue config with branch/glob `base`, unique
  queue names, branch-scoped overlap diagnostics, default-branch policy authority
  and per-repository `delete_head_branch`. Serving still uses `--repos` and one
  `--base`; other configured queues are logged as `not yet served`. The existing
  single-queue `main` config and queue key are preserved.
- [x] PR 2: multiple base workers, branch lifecycle and settlement-first cycles.
- [x] PR 3: installation discovery, canonical keys, removal/suspension,
  installation budgets and discovery status.
- [x] PR 4: webhook health, backstop scheduling, onboarding and migration docs.

### Phase 2 — Throughput

- [x] Speculative chain of batches with a **Zuul-style AIMD window** (grows on landing, halves once per failed batch), landing strictly in chain order through the existing fence; only the oldest unlanded batch is blamed ([ADR 0009](docs/adr/0009-speculative-batching.md)); `max_depth` defaults to 1 until the adopter profiles pass and two weeks of dogfooding at depth 4 are no worse than an adjacent depth-1 period ([plan](docs/research/phase2-dogfood.md)), then 4
- [x] Opt-in dynamic batch sizing from observed failure rate (`batch.target_pass_rate`)
- [x] Bounded same-commit check retries before a failure counts (a prerequisite for speculation); optimistic failure resolution deferred
- [x] Optional CI cancellation capability (`actions: write` on GitHub) and a per-change CI run ledger
- [ ] Priorities (`priority_rules`, numeric levels, optional preemption) and multiple queues
- [ ] Check timeouts (automatic, p95-based)
- [ ] Pause, scheduled freezes with exclusions, `schedule` conditions
- [ ] Read-only web dashboard: queue graph, batch history, why-is-my-PR-stuck explainer
- [ ] **MCP server** exposing queue status, enqueue/dequeue, failure explanations

**Exit:** the strict adopter profiles in [ADR 0009](docs/adr/0009-speculative-batching.md) pass in simulation (20 changes/day with 30-minute CI: p95 time to merge under 60 minutes; 5 changes/day with 3-hour CI: p95 under 4.5 hours; both under 1.5 CI runs per merged change), and the simulator publishes strict capacity across defect rates. The earlier 2,000 PRs/day strict target is superseded: strict capacity is bounded by failure resolution rather than depth, and Phase 2b's exit covers high volume.

**Strict batching exit achieved:** ADR 0009's planner-backed Team and Slow gates
pass in both cancellation modes, and all eight paired adaptive-sizing seeds pass
the cost/median gate plus the added p95 regression assertion. The capacity curve,
window equilibrium and day-1 results are published in
[the results report](docs/research/phase2-chain.md). The remaining unchecked
Phase 2 items above stay open. Default depth stays **1** until the separate
ADR 0009 dogfooding criteria are met.

### Phase 2b — Post-merge verification mode

Built after Phase 1 closes, in parallel with Phase 2 where they don't overlap ([ADR 0005](docs/adr/0005-verification-modes.md)).

- Per-queue `verification: post_merge` with `max_base_age`, `on_red`, `max_red` and `max_open_suspects`; admission on a pinned head, fresh trust gate at landing, composition on the current tip, and an honest tested-as/landed-as record (new event types and fence form)
- **Per-defect suspects:** one rerun, suspects from per-commit CI (verify refs run every skipped commit in parallel), one verified automatic revert per suspect that skips admission, then escalation with ordinary landings paused
- **Green ref** `openmerge/green/<base>` for deploys: the newest fully passing base commit inside the verification frontier, or a tested deploy candidate with every open suspect reverted
- **Conflict events:** structured conflict reports to the author; the fixed push re-enters at the back until change identity (Phase 3)
- Simulator: post-merge model with a finite CI runner pool, injected bad changes, interacting culprits and flakes

**Exit:** at 30,000 simulated PRs/day, 30-minute CI, a 2,000-runner pool and 1% independent bad changes that each break required checks no other live defect breaks, a post-merge queue keeps median time-to-merge under CI duration plus 10 minutes and CI runs per merged PR under 2.5. The green ref only ever points at a commit on which every required check passed. With per-commit CI results available, every such bad change is reverted, with the revert verified, within 4x CI duration. Defects that share checks are remedied through residual search or escalate within `max_red`, as do interacting or inconclusive cases. Given a uniform arrival profile, the green ref's base commit is never more than 6x CI duration behind the tip, counting failed candidates.

### Phase 3 — Agent-native

Phase 3 treats the change, not the pull request, as the unit of work, in three stages ([ADR 0007](docs/adr/0007-changes-not-prs.md)). Stage A works entirely on pull requests; Stages B and C follow once their prerequisites exist.

Stage A's [implementation plan](docs/adr/0007-changes-not-prs.md#implementation)
uses 18 PRs across 12 workstreams, starting with four independent steps: 1, 2a,
3a and 5. It records dependencies, resolved maintainer decisions, rollout and
rollback boundaries, and [trial deployment holds](docs/adr/0007-changes-not-prs.md#trial-deployment).
Stage A acceptance also requires the separate outside-pushes work in
[PR #53](https://github.com/CanadaApollo6/openmerge/pull/53), branch
`adr-0007/outside-pushes`. Conflict grace is excluded from Stage A acceptance.

- **Change identity (A):** a server-assigned change with a durable revision sequence, including H1→H2→H1; a new head leaves the queue as today, and a started landing always settles first. Conflicts remove work without grace; cancellation remains speculative-only
- **Classification (A):** conservative labels (`docs`, `generated`, `owned:<owner>`, `policy`, `unknown`) pinned to base and head; anything uncertain is `unknown`; comment-only detection deferred
- **Policy approvals (A):** opt-in, agent-aware rules (policy-bot style) that only add requirements on top of `enter_when`/`merge_when`, e.g. agent-authored changes touching `infra/**` need two approvals from a group; unknown matches never lower requirements; head-bound native reviews, `@openmerge approve <brief-digest>` receipts that also bind intent, no self-approval, and a decision snapshot in the landing-start gate; changes waiting for approval are skipped by batching instead of holding a batch
- **Accountability (A):** separate actor, accountable human and reviewer identities; agents map to accountable humans in default-branch config
- **Pushes outside the queue (A):** observed base moves OpenMerge did not make are recorded as landed outside the queue, with no OpenMerge verification before landing
- **Review briefs (A):** templated reviewer, operator and agent views with a file and hunk inventory (labelled incomplete when it is); receipt approvals record the brief's digest
- Per-author and per-agent concurrency, CI-spend, and retry budgets, with escalation to a human
- **Policy authority ([ADR 0008](docs/adr/0008-openmerge-policy-authority.md)):** Part 1, the read-only `setup import-protection`, is implemented. Stage A's [completed organization-sandbox acceptance](docs/research/stage-a-acceptance.md) unblocks **planned Parts 2 and 3**, not a live deployment. The [implementation plan](docs/adr/0008-openmerge-policy-authority.md#implementation-plan) has 13 PRs in 11 workstreams:
  - **Part 2:** 1 candidate authority/digest/wire contracts (Inert); 2 visible forge observations and fake/real parity (Inert); 3a ref compilation and content-rule validation (Inert); 3b reversible `openmerge-base`/`openmerge-refs` setup (Inert); 4 durable `write_restriction` and verification (Inert); 5 outside-push warnings and acknowledgement (Inert); 6 `/v1/live`, `/v1/health`, and reconciliation deadlines (Hold); 7 PR check queue instructions (Hold); 8 atomic per-check landing evidence (Hold); 9 schema/loader publication and complete authority gate (Hold).
  - **Part 3:** 10a deterministic read-only audit export (Inert); 10b retention, reproducibility, and external-anchor acceptance (Inert).
  - **Joint exit:** 11 Smart-Data-Ohio organization-sandbox switch, break-glass, drift, rollback, and real audit evidence (Inert docs/evidence; requires held implementation in the isolated sandbox). PR 9 activates after 3b and 4–8. Part 2 exits only after 11; Part 3 also requires 10b and 11's actual export.
  - Classes follow the [trial deployment policy](docs/adr/0008-openmerge-policy-authority.md#trial-deployment): Hold waits until ADR 0009 period B drains. Inert needs differential replay and deployable dependencies. Live App permissions remain frozen; Administration read adoption on the live App waits until after the trial. Admin-write remains on the separate setup token.
- Structured failure explanations: which batch, which check, which tests, flaky or real, suggested next action
- **Conflict grace:** with change identity, a conflicting change may hold its position for one non-renewable `conflict_grace` with bounded retries, while entries behind it keep landing
- **Write API and lookups agents need:** enqueue/dequeue/refresh over the API, and "which commit landed PR N" without scanning history
- **Direct submission (B):** after a user authentication and authorization ADR, `openmerge change submit` pushes `HEAD` to `openmerge/submit/<id>`; `changes.pull_requests: when_review_needed` per queue, never landing while the base's protection requires a pull request
- **PR-less queues and generated prose (C):** `changes.pull_requests: never` once notifications and request-changes work outside pull requests; optional, labelled model summaries in briefs
- A ready-made **queue-watcher agent skill** that reads the structured logs and "why" API and reports anomalies
- Outbound webhooks and OpenTelemetry export for every queue event
- Agent skills and docs written for agents (`llms.txt`, Markdown docs, stable `--json` and exit codes)

**Exit (on the fake forge):** for Stage A, an unattended coding agent opens a pull request and takes it to landed or definitively rejected using only the CLI or MCP; a low-risk change lands on policy alone, a change that needs a human waits until a scripted reviewer approves its exact head, and every landing's decision snapshot can be read back. For Stage B, the same starting from `openmerge change submit` with no pull request.

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
- High availability (multiple workers, leader-free queue ownership via row locks), horizontal scaling
- Security review, threat model, signed releases, SBOM
- Config schema 1.0 with a compatibility promise

**Exit:** 1.0 release; a public deployment handling at least 10,000 merges/day.

## Gating modes (the spectrum OpenMerge supports)

| Mode | Main is | Throughput | When | Phase |
|---|---|---|---|---|
| Strict serial | always green | lowest | small repos, regulated code | 1 |
| Batched + speculative (default) | always green | high | most teams | 2 |
| Optimistic | green except rare intermittent bugs | higher | high flake rates | 2 |
| Post-merge verify + auto-revert | briefly red, auto-healed; deploy from the green ref | highest | agent-scale repositories | 2b |
| Lanes (scopes) | green per lane | scales with independence | monorepos | 4 |

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
