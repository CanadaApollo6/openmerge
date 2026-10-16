# 0006. Serve every installed repository from one process, driven by webhooks with polling as the backstop

- **Status:** Proposed
- **Date:** 2026-10-16

## Context

Phase 1 runs one `openmerge serve` per installation, with the repositories and base branch given as flags, one queue per repository, and polling as the only reliable input. That was enough to merge OpenMerge's own PRs. The first real adopters are two work repositories (10 to 20 PRs a day, 30 to 40 minute CI) and a personal repository with very slow CI. A team should be able to install the App and commit a config file without anyone editing a service unit.

Three Phase 1 limits stand in the way: repositories and the base branch are process flags; version 1 config accepts exactly one queue; and webhooks only trigger an early poll, with no notion of delivery health.

## Decision

### Configuration authority

**The default branch's `.openmerge.yml` governs every queue in a repository.** This amends [ADR 0003](0003-configuration-format.md), which reads config from each queue's base branch. With several bases, per-base config would let anyone who can push to a release branch weaken that branch's queue. OpenMerge reads the file at the observed default-branch tip, records that commit with the configuration it parsed, and reloads when the default branch moves. A PR never changes the rules it is judged by. For a repository whose only queue is on its default branch, as in every Phase 1 deployment, nothing changes. A missing, invalid or unreadable file pauses that repository's queues; it never falls back to a guess.

### Discovery and identity

**The App's installations define what is served.** One process serves one App. At startup, every `discovery_interval` (default 10 minutes), and on `installation` and `installation_repositories` webhooks, `serve` lists the App's installations and their repositories. A repository is served when its default branch has `.openmerge.yml` and it passes the optional `--repos` allowlist (owner/name globs, which only narrow). A webhook only triggers a listing; it never adds or removes a repository on its own.

**Existing queues keep their identity.** A queue's key stays (installation, repository, base branch, lane) as in [ADR 0002](0002-storage-and-state-model.md): the installation ID as a decimal string, the repository's `owner/name` compared case-insensitively against stored keys and reused exactly as stored, the base branch name, and the empty default lane. A queue's YAML `name` is not part of the key. Registering a queue is idempotent and atomic in the store, and one worker runs per key however many discovery paths race to register it. The live Phase 1 queue is therefore found, not recreated, and its log and fence carry on.

**Removal never pretends a landing failed.** A repository is removed only after a successful listing omits it, its config file is deleted, or the allowlist drops it; a failed listing or read changes nothing. A suspended installation is treated the same way. A removed queue stops admitting work and reports `paused: repository-removed` where it still can. An attempt that started landing stays recorded until it is settled through the fence. If credentials are gone, the queue waits, unsettled and blocking further landings, until access returns. Token loss is never evidence that a landing was abandoned.

### Queues and branches

**One queue per base branch.** Version 1 config accepts more than one entry under `queues`, each with an optional `base`: a branch name or glob, defaulting to the default branch. Allowing more entries and adding an optional key are additive under ADR 0003, so existing files keep working; files that use them are rejected by older binaries, so downgrading past this change means removing the extra queues first. Queue names must be unique within the file.

**Branches come and go safely.** OpenMerge expands `base` globs at discovery and on `create` and `delete` webhooks. A newly matching branch becomes a queue only after its selector is checked for overlaps and its protection rules pass. A branch matched by two queues is a configuration error for those queues only; other queues keep running. When a base branch is deleted, its queue goes dormant and keeps its log and fence. If the branch is recreated, any unsettled attempt is resolved through the fence before the queue admits work again.

**Settlement runs first and on its own.** Each worker cycle resolves in-flight landings before anything else, and a configuration error, read failure or pause never skips that step.

### Inputs

**Webhooks wake queues; polling remains the source of truth.** With a webhook listener configured, every delivery whose signature verifies records its observation and wakes the affected queue at once. Every decision that matters (trust, CI, landing) is still re-read from the forge. Polling continues for every queue. A queue with a batch in testing or landing, or with an unsettled attempt, polls at `poll_interval` (default 1 minute) whatever the webhook health. Other queues poll at `poll_interval` when webhooks are not configured or are unhealthy, and at `backstop_interval` (default 10 minutes) while they are healthy.

**Only verified deliveries count toward health.** Webhooks are healthy for an installation while a verified delivery for it has arrived within `webhook_health_window` (default 30 minutes), and the window restarts after every listener restart. A quiet installation falls back to `poll_interval`, which costs little because its queues are idle. Requests that fail signature verification never affect health or scheduling. They appear only as a bounded counter in logs and metrics. Redeliveries are deduplicated by delivery ID as today.

**Polling is a fine default.** With 30 to 40 minute CI, a one-minute poll adds about 2% to time-to-merge. Webhooks matter most for slow-CI repositories, where every transition otherwise waits for a poll, and for busier installations.

### Budget

**Each installation has its own rate budget, with room reserved for settlement.** The reconciler tracks budget per installation, as GitHub limits each installation token separately. Requests are prioritized: settling landings first, then queues in testing or landing, then idle polling, discovery, rules checks and reporting. The last 10% of an installation's budget is reserved for settlement. A central cross-queue scheduler is not part of this decision; per-installation drivers already isolate installations from each other.

### Per-repository settings and visibility

**Repository behavior lives in the repository.** Settings that differ between teams move into `.openmerge.yml` as additive keys, starting with `delete_head_branch`. The server flag becomes the default for repositories that do not set it.

**Discovery is observable.** The status API and CLI list every repository the App can see, with its state (served, not configured, invalid config, removed, suspended, dormant base), the last successful sweep, and the remaining budget per installation. OpenMerge does not comment on repositories that have no config file.

### Onboarding and migration

**Onboarding a repository:** install the App on it (an organization owner may need to approve), run `openmerge setup rulesets` for it with a repository-admin token, make sure CI runs on pushes to `openmerge/queue/**`, and commit `.openmerge.yml` to the default branch. A private App can only be installed on its owner's account, so an organization needs its own App (`openmerge setup github --owner ORG`) and its own `serve` process, which can share the database.

**Migrating a Phase 1 deployment:** upgrade with the existing flags, which now act as an allowlist. Confirm in `openmerge queue status` that the existing queue appears with its history. Then remove `--installation-id`, `--repos` and `--base` and restart. The flags stay supported as allowlists, with a deprecation warning naming the replacement.

## Consequences

- Onboarding a repository needs no change to the server. Removing it stops OpenMerge without abandoning in-flight work.
- Release branches get their own queues under one policy file, which only default-branch writers can change.
- Webhooks cut idle polling by about `backstop_interval / poll_interval` and cut transition latency from up to a poll interval to seconds for idle queues, while active queues keep their one-minute safety net.
- The simulator and store gain multi-queue and lifecycle tests: concurrent registration of an existing key, removal and reinstatement mid-landing, branch deletion and recreation, overlapping globs, and unsigned-request floods.
- Implementation lands in four PRs: (1) multi-queue config with per-queue `base`, the config-authority change and per-repository settings, served from flags; (2) multiple base-branch workers per repository with branch lifecycle and settlement-first cycles; (3) installation discovery, canonical keys, removal and suspension, budget per installation, and discovery status; (4) webhook health and backstop scheduling, plus onboarding and migration docs.

## Alternatives considered

- **One process per repository, managed by the operator.** It already works, but it puts a deployment step in front of every repository and multiplies connections and caches.
- **Per-base config files.** Each release branch could carry its own policy, but anyone who can push to that branch could weaken its queue.
- **A version 2 config.** Not needed: per-queue `base` fits version 1 as an additive change, and a version bump would force every user to migrate.
- **Webhooks only.** GitHub does not guarantee delivery, and many self-hosters have no public endpoint. A missed delivery would leave a PR stuck.
- **A server-side list of repositories.** It reintroduces the per-repository deployment step; the allowlist covers operators who want explicit control.
