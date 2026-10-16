# 0006. Serve every installed repository from one process, driven by webhooks with polling as the backstop

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

Phase 1 runs one `openmerge serve` per installation, with the repositories listed on the command line, one queue per repository, and polling as the only reliable input. That was enough to merge OpenMerge's own PRs. It is not enough for the first real adopters: two work repositories (10 to 20 PRs a day, 30 to 40 minute CI) and a personal repository with very slow CI. A team should install the App on the repositories it wants and have them served, without editing a service unit for each one.

Three Phase 1 limits stand in the way:

- **Configuration is per process.** Repositories, the base branch and the installation are flags. Adding a repository means restarting the server with new flags.
- **One queue per repository.** Version 1 config accepts exactly one queue, for the base branch given on the command line. Repositories with release branches need a queue per base.
- **Polling is the source of truth.** A 30-second poll costs latency on every transition and a share of the rate budget for every open PR. Webhooks exist but only trigger an early poll, and their delivery health is not tracked.

## Decision

### Discovery

**The App's installations define what is served.** At startup and every `discovery_interval` (default 10 minutes), and on `installation` and `installation_repositories` webhooks, `serve` lists the App's installations (`GET /app/installations`) and each installation's repositories. A repository is served when its default branch contains `.openmerge.yml`. An optional `--repos` allowlist (owner/name globs) narrows this; it never widens it.

**Removal is graceful.** When a repository leaves an installation, its `.openmerge.yml` is deleted, or the allowlist drops it, OpenMerge stops admitting work, lets in-flight landings resolve through the fence, reports `paused: repository-removed` on queued PRs it can still reach, and keeps the event log. A suspended installation pauses all its queues the same way.

### Queues

**Multiple queues per repository, one per base branch.** Version 1 config accepts more than one entry under `queues`, each with a new optional `base` key: a branch name or glob, defaulting to the repository's default branch. Allowing more entries and adding an optional key are additive changes under [ADR 0003](0003-configuration-format.md), so version 1 configs keep working unchanged. Every queue's `base` must match a distinct set of existing branches; a branch matched by two queues is a configuration error. A PR joins the queue for its base branch. Several queues on the same base (priorities and lanes) remain Phase 2 and Phase 4 work.

**A queue's identity does not change.** Queue keys stay (installation, repository, base branch, lane) as in [ADR 0002](0002-storage-and-state-model.md). A glob creates one queue per matching branch, each with its own fence and event log. The existing live queue keeps its key, so its stored log continues unchanged.

### Inputs

**Webhooks first, polling as the backstop.** With a webhook listener configured, every verified delivery for a PR, check, status, review, comment or push records its observation and wakes the affected queue at once, instead of waiting for the next poll. Polling continues for every queue, at `poll_interval` (default 1 minute) when webhooks are not configured or are unhealthy, and at `backstop_interval` (default 10 minutes) while they are healthy. Webhooks are healthy while deliveries keep arriving and none of the last ten has failed signature verification; GitHub's ping and redelivery count as deliveries. A webhook never carries state that polling cannot confirm: deliveries are triggers plus observations, and every decision that matters (trust, CI, landing) is re-read as today.

**Polling stays fully supported** for deployments without a public endpoint, like the current self-hosted one. The docs cover the usual ways to get a public endpoint (a reverse proxy, a tunnel).

### Budget and isolation

**Each installation has its own rate budget.** GitHub rate-limits installation tokens per installation, so the reconciler tracks budget per installation and shares it fairly between that installation's queues (round robin, with queues that have work in testing or landing going first). One busy repository can slow its siblings in the same installation, never another installation.

**One process serves everything; queues stay serialized.** Per-queue advisory locks ([ADR 0002](0002-storage-and-state-model.md)) already let one process run many queues concurrently, bounded by `concurrency`. Running several processes against the same database for availability is not part of this decision.

### Configuration

**Process flags shrink to deployment concerns.** `serve` needs the App ID, private key, database URL and git cache directory, plus optional webhook listener and secret, status API, intervals, concurrency and allowlist. `--installation-id`, `--repos` (as a required list) and `--base` are deprecated: if given, they act as an allowlist and a warning names the replacement. Everything about a repository's queues comes from its `.openmerge.yml`.

## Consequences

- Installing the App on a repository and committing `.openmerge.yml` is the whole onboarding for a team. Removing either stops OpenMerge cleanly.
- Webhooks cut queue latency from up to a poll interval to seconds, and cut steady-state API use by about a factor of `backstop_interval / poll_interval`. Polling remains the correctness mechanism, so a lost delivery still only costs latency.
- Setup (`openmerge setup rulesets`) and startup rules checks run per served repository and per base, including ruleset coverage for every base a glob matches and an `openmerge/fence/*` rule that covers all of them.
- Discovery adds two paginated reads per installation per `discovery_interval`, plus one config read per repository on the default branch, cached with conditional requests.
- The status API and CLI list every served queue, grouped by repository, and include the discovery state (served, not configured, removed, suspended).
- The simulator and store gain multi-queue tests: independent queues progress concurrently, a slow queue does not delay another installation, and removal mid-landing resolves through the fence.

## Alternatives considered

- **Keep one process per repository, managed by the operator.** Simple, and it already works, but it puts a deployment step in front of every new repository and multiplies database connections and git caches.
- **A version 2 config with a new top-level layout.** Not needed: per-queue `base` fits version 1 as an additive change, and a version bump would force every user to migrate.
- **Webhooks only.** Lower API use, but a missed delivery would leave a PR stuck with no recovery. GitHub does not guarantee delivery, and many self-hosters have no public endpoint.
- **Repository lists in a server-side file instead of discovery.** Reintroduces the per-repository deployment step. The allowlist covers operators who want explicit control.
